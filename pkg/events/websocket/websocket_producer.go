package websocket_producer

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/gomessguii/logger"
	"github.com/gorilla/websocket"
)

const (
	sessionQueueSize = 128
	writeWait        = 10 * time.Second
	pongWait         = 60 * time.Second
	pingPeriod       = pongWait * 9 / 10
	maxInboundBytes  = 64 << 10
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		logger.LogInfo("Verificando origem da conexão WebSocket")
		return true
	},
	// The auth token is carried as the second Sec-WebSocket-Protocol value
	// (["apikey", "<token>"]). Advertise only "apikey" so the handshake echoes
	// that scheme back and never reflects the token in the response header.
	Subprotocols: []string{"apikey"},
}

type websocketSession struct {
	id         uint64
	instanceID string
	conn       *websocket.Conn
	send       chan []byte
	done       chan struct{}
	closeOnce  sync.Once
	producer   *websocketProducer
}

type websocketProducer struct {
	clients       map[string]map[uint64]*websocketSession
	broadcast     map[uint64]*websocketSession
	clientsMux    sync.RWMutex
	nextSessionID atomic.Uint64
	closed        bool
	loggerWrapper *logger_wrapper.LoggerManager
}

func NewWebsocketProducer(loggerWrapper *logger_wrapper.LoggerManager) *websocketProducer {
	return &websocketProducer{
		clients:       make(map[string]map[uint64]*websocketSession),
		broadcast:     make(map[uint64]*websocketSession),
		loggerWrapper: loggerWrapper,
	}
}

// TokenFromProtocolHeader extracts the auth token from a Sec-WebSocket-Protocol
// header of the form "apikey, <token>". Browsers can't set custom headers on a
// WebSocket handshake but can set subprotocols via `new WebSocket(url, [...])`,
// so the token travels there instead of the query string. Returns "" when the
// header is absent or not in the expected shape.
func TokenFromProtocolHeader(header string) string {
	protocols := strings.Split(header, ",")
	if len(protocols) >= 2 && strings.TrimSpace(protocols[0]) == "apikey" {
		return strings.TrimSpace(protocols[1])
	}
	return ""
}

// ServeWs upgrades and registers one independent session. A caller closing one
// browser tab cannot replace or remove another tab for the same instance.
func ServeWs(w http.ResponseWriter, r *http.Request, instanceID string, producer *websocketProducer) {
	logger.LogInfo("Iniciando upgrade da conexão WebSocket")
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.LogError("Erro ao fazer upgrade da conexão websocket: %v", err)
		return
	}

	session, added := producer.addSession(instanceID, conn)
	if !added {
		_ = conn.Close()
		return
	}
	logger.LogInfo("Conexão WebSocket estabelecida com sucesso")
	go session.writePump()
	go session.readPump()
}

func (p *websocketProducer) addSession(instanceID string, conn *websocket.Conn) (*websocketSession, bool) {
	session := &websocketSession{
		id:         p.nextSessionID.Add(1),
		instanceID: instanceID,
		conn:       conn,
		send:       make(chan []byte, sessionQueueSize),
		done:       make(chan struct{}),
		producer:   p,
	}

	p.clientsMux.Lock()
	if p.closed {
		p.clientsMux.Unlock()
		return session, false
	}
	if instanceID == "" {
		p.broadcast[session.id] = session
	} else {
		if p.clients[instanceID] == nil {
			p.clients[instanceID] = make(map[uint64]*websocketSession)
		}
		p.clients[instanceID][session.id] = session
	}
	p.clientsMux.Unlock()

	if instanceID == "" {
		logger.LogInfo("Cliente broadcast websocket adicionado")
	} else {
		p.logInstanceInfo(instanceID, "Cliente websocket adicionado para instância: %s", instanceID)
	}
	return session, true
}

func (p *websocketProducer) removeSession(session *websocketSession) {
	if p == nil || session == nil {
		return
	}
	p.clientsMux.Lock()
	removed := false
	if session.instanceID == "" {
		if current := p.broadcast[session.id]; current == session {
			delete(p.broadcast, session.id)
			removed = true
		}
	} else if sessions := p.clients[session.instanceID]; sessions != nil {
		if current := sessions[session.id]; current == session {
			delete(sessions, session.id)
			removed = true
		}
		if len(sessions) == 0 {
			delete(p.clients, session.instanceID)
		}
	}
	p.clientsMux.Unlock()

	session.close()
	if !removed {
		return
	}
	if session.instanceID == "" {
		logger.LogInfo("Cliente broadcast websocket removido")
	} else {
		p.logInstanceInfo(session.instanceID, "Cliente websocket removido para instância: %s", session.instanceID)
	}
}

func (session *websocketSession) close() {
	session.closeOnce.Do(func() {
		close(session.done)
		if session.conn != nil {
			_ = session.conn.Close()
		}
	})
}

func (session *websocketSession) enqueue(message []byte) bool {
	select {
	case <-session.done:
		return false
	default:
	}
	select {
	case session.send <- message:
		return true
	case <-session.done:
		return false
	default:
		return false
	}
}

func (session *websocketSession) readPump() {
	defer session.producer.removeSession(session)
	session.conn.SetReadLimit(maxInboundBytes)
	_ = session.conn.SetReadDeadline(time.Now().Add(pongWait))
	session.conn.SetPongHandler(func(string) error {
		return session.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		if _, _, err := session.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (session *websocketSession) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		session.producer.removeSession(session)
	}()
	for {
		select {
		case message := <-session.send:
			if err := session.write(websocket.TextMessage, message); err != nil {
				session.producer.logInstanceError(session.instanceID, "WebSocket session %d write failed: %v", session.id, err)
				return
			}
		case <-ticker.C:
			if err := session.write(websocket.PingMessage, nil); err != nil {
				session.producer.logInstanceError(session.instanceID, "WebSocket session %d ping failed: %v", session.id, err)
				return
			}
		case <-session.done:
			return
		}
	}
}

func (session *websocketSession) write(messageType int, message []byte) error {
	if err := session.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return session.conn.WriteMessage(messageType, message)
}

func (p *websocketProducer) Produce(queueName string, payload []byte, instanceID string, _ string) error {
	message, err := json.Marshal(map[string]interface{}{
		"queue":   strings.ToLower(queueName),
		"payload": string(payload),
	})
	if err != nil {
		return err
	}

	sessions := p.snapshotSessions(instanceID)
	var dropped int
	for _, session := range sessions {
		if !session.enqueue(message) {
			dropped++
			p.removeSession(session)
		}
	}
	if dropped > 0 {
		p.logInstanceError(instanceID, "Disconnected %d slow websocket session(s)", dropped)
	}
	return nil
}

func (p *websocketProducer) snapshotSessions(instanceID string) []*websocketSession {
	p.clientsMux.RLock()
	count := len(p.broadcast)
	if sessions := p.clients[instanceID]; sessions != nil {
		count += len(sessions)
	}
	result := make([]*websocketSession, 0, count)
	for _, session := range p.clients[instanceID] {
		result = append(result, session)
	}
	for _, session := range p.broadcast {
		result = append(result, session)
	}
	p.clientsMux.RUnlock()
	return result
}

// Close terminates all hijacked connections during application shutdown.
func (p *websocketProducer) Close() {
	if p == nil {
		return
	}
	p.clientsMux.Lock()
	if p.closed {
		p.clientsMux.Unlock()
		return
	}
	p.closed = true
	sessions := make([]*websocketSession, 0, len(p.broadcast))
	for _, session := range p.broadcast {
		sessions = append(sessions, session)
	}
	for _, instanceSessions := range p.clients {
		for _, session := range instanceSessions {
			sessions = append(sessions, session)
		}
	}
	p.broadcast = make(map[uint64]*websocketSession)
	p.clients = make(map[string]map[uint64]*websocketSession)
	p.clientsMux.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

func (p *websocketProducer) logInstanceInfo(instanceID, format string, args ...interface{}) {
	if p.loggerWrapper == nil {
		logger.LogInfo(format, args...)
		return
	}
	p.loggerWrapper.GetLogger(instanceID).LogInfo(format, args...)
}

func (p *websocketProducer) logInstanceError(instanceID, format string, args ...interface{}) {
	if p.loggerWrapper == nil {
		logger.LogError(format, args...)
		return
	}
	p.loggerWrapper.GetLogger(instanceID).LogError(format, args...)
}

func (p *websocketProducer) CreateGlobalQueues() error {
	return nil
}
