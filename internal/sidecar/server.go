package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/eventbridge"
	"github.com/jtsang4/larky/internal/larkevent"
	requestsvc "github.com/jtsang4/larky/internal/request"
	"github.com/jtsang4/larky/internal/router"
	"github.com/jtsang4/larky/internal/state"
)

var ErrAlreadyRunning = errors.New("larky sidecar is already running")

var requiredEventKeys = []string{"card.action.trigger", "im.message.receive_v1"}

type Options struct {
	DisableEvents bool
	Logger        *log.Logger
}

type Server struct {
	cfg              config.Config
	store            *state.Store
	requests         *requestsvc.Service
	router           *router.Router
	logger           *log.Logger
	startedAt        time.Time
	cancel           context.CancelFunc
	executableDigest string

	mu            sync.Mutex
	subscriptions map[string]struct{}
	eventsEnabled bool
	eventReady    map[string]bool
}

func Run(ctx context.Context, cfg config.Config, options Options) error {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("create sidecar state directory: %w", err)
	}
	lockPath := filepath.Join(cfg.StateDir, "sidecar.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return ErrAlreadyRunning
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	socketPath := cfg.SocketPath()
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale sidecar socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on sidecar socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("secure sidecar socket: %w", err)
	}

	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	logger := options.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "larky: ", log.LstdFlags|log.Lmicroseconds)
	}
	store := state.New(cfg.DatabasePath())
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve sidecar executable: %w", err)
	}
	digest, err := fileDigest(executable)
	if err != nil {
		return fmt.Errorf("digest sidecar executable: %w", err)
	}
	eventsEnabled := !options.DisableEvents && os.Getenv("LARKY_EVENT_SOURCE") != "disabled"
	server := &Server{
		cfg: cfg, store: store, requests: requestsvc.NewService(store, cfg), router: router.New(store), logger: logger,
		startedAt: time.Now().UTC(), cancel: cancel, executableDigest: digest,
		subscriptions: make(map[string]struct{}),
		eventsEnabled: eventsEnabled, eventReady: make(map[string]bool),
	}
	logger.Printf("sidecar ready pid=%d socket=%s", os.Getpid(), socketPath)

	if eventsEnabled {
		consumer := eventbridge.Consumer{
			CLI: cfg.LarkCLI, Identity: cfg.EventIdentity, Logger: logger,
			OnEvent: server.processRawEvent, OnState: server.setEventState,
		}
		for _, eventKey := range requiredEventKeys {
			go consumer.Run(serverCtx, eventKey)
		}
	}
	go func() {
		<-serverCtx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if serverCtx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			logger.Printf("accept connection: %v", err)
			continue
		}
		go server.handleConnection(serverCtx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	decoder := json.NewDecoder(bufio.NewReader(conn))
	var cmd command
	if err := decoder.Decode(&cmd); err != nil {
		s.writeResponse(conn, response{OK: false, Error: "invalid command: " + err.Error()})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch cmd.Op {
	case "ping":
		s.writeResponse(conn, response{OK: true})
	case "status":
		status, err := s.status()
		if err != nil {
			s.writeResponse(conn, response{OK: false, Error: err.Error()})
			return
		}
		s.writeResponse(conn, response{OK: true, Status: &status})
	case "stop":
		s.writeResponse(conn, response{OK: true})
		s.cancel()
	case "publish":
		result, err := s.process(cmd.EventKey, cmd.Event, cmd.Synthetic)
		if err != nil && !errors.Is(err, router.ErrDuplicate) && !errors.Is(err, router.ErrUnrouted) {
			s.writeResponse(conn, response{OK: false, Error: err.Error(), Result: result})
			return
		}
		s.writeResponse(conn, response{OK: true, Result: result})
	case "subscribe":
		s.subscribe(ctx, conn, cmd.Platform, cmd.SessionID)
	default:
		s.writeResponse(conn, response{OK: false, Error: "unsupported operation"})
	}
}

func (s *Server) subscribe(ctx context.Context, conn net.Conn, platform contract.Platform, sessionID string) {
	if platform != contract.PlatformClaude || sessionID == "" {
		s.writeResponse(conn, response{OK: false, Error: "only Claude subscriptions with an exact session_id are supported"})
		return
	}
	key := state.InboxKey(platform, sessionID)
	s.mu.Lock()
	if _, exists := s.subscriptions[key]; exists {
		s.mu.Unlock()
		s.writeResponse(conn, response{OK: false, Error: "this session already has an active subscriber"})
		return
	}
	s.subscriptions[key] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subscriptions, key)
		s.mu.Unlock()
	}()
	if !s.writeResponse(conn, response{OK: true}) {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	heartbeat := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if !s.writeResponse(conn, response{OK: true}) {
				return
			}
		case <-ticker.C:
			item, err := s.peekInbox(key)
			if err != nil {
				s.logger.Printf("read Claude inbox %s: %v", key, err)
				continue
			}
			if item == nil {
				continue
			}
			if !s.writeResponse(conn, response{OK: true, Reply: &item.Reply}) {
				return
			}
			if err := s.requests.AcknowledgeReplyHandoff(item.Reply, contract.HandoffClaudeMonitor); err != nil {
				s.logger.Printf("ack Claude inbox %s: %v", key, err)
			}
		}
	}
}

func (s *Server) processRawEvent(eventKey string, raw []byte) {
	result, err := s.process(eventKey, raw, false)
	if err != nil {
		s.logger.Printf("event %s was not dispatched: %v", eventKey, err)
		return
	}
	if result.Disposition != "" {
		s.logger.Printf("event %s disposition=%s", eventKey, result.Disposition)
	}
}

func (s *Server) process(eventKey string, raw []byte, synthetic bool) (router.Result, error) {
	event, err := larkevent.Normalize(eventKey, raw, time.Now())
	if err != nil {
		return router.Result{}, err
	}
	event.Synthetic = synthetic
	if synthetic {
		event.Source = "fixture"
	}
	return s.router.Handle(event)
}

func (s *Server) writeResponse(conn net.Conn, value response) bool {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := json.NewEncoder(conn).Encode(value)
	_ = conn.SetWriteDeadline(time.Time{})
	return err == nil
}

func (s *Server) status() (Status, error) {
	status := Status{
		PID: os.Getpid(), StartedAt: s.startedAt.Format(time.RFC3339), ExecutableDigest: s.executableDigest,
		EventsEnabled: s.eventsEnabled, EventConsumers: make(map[string]bool), PendingByKind: make(map[string]int),
	}
	s.mu.Lock()
	status.Subscribers = len(s.subscriptions)
	status.EventsReady = s.eventsEnabled
	for _, eventKey := range requiredEventKeys {
		ready := s.eventReady[eventKey]
		status.EventConsumers[eventKey] = ready
		status.EventsReady = status.EventsReady && ready
	}
	s.mu.Unlock()
	err := s.store.View(func(db *state.Database) error {
		for _, req := range db.Requests {
			status.PendingByKind[string(req.State)]++
		}
		for _, items := range db.Inbox {
			status.PendingByKind["queued_reply"] += len(items)
		}
		return nil
	})
	return status, err
}

func (s *Server) setEventState(eventKey string, ready bool) {
	s.mu.Lock()
	s.eventReady[eventKey] = ready
	s.mu.Unlock()
}

func (s *Server) peekInbox(key string) (*state.InboxItem, error) {
	var result *state.InboxItem
	err := s.store.View(func(db *state.Database) error {
		items := db.Inbox[key]
		if len(items) == 0 {
			return nil
		}
		copy := *items[0]
		result = &copy
		return nil
	})
	return result, err
}
