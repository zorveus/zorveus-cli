package statestore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

type Lock struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
}

type Document struct {
	Version  int             `json:"version"`
	ID       string          `json:"id"`
	Revision uint64          `json:"revision"`
	Lock     *Lock           `json:"lock,omitempty"`
	Data     json.RawMessage `json:"data"`
}

type Session struct {
	backend  Backend
	Document Document
	version  string
	failed   bool
}

func Read(ctx context.Context, backend Backend) (Document, string, error) {
	object, err := backend.Read(ctx)
	if err != nil {
		return Document{}, "", err
	}
	if !object.Exists {
		return Document{}, "", problem.New("remote_state_missing", "The configured durable state does not exist. Run zorveus state init once to initialize it. A missing production state is never treated as an empty deployment.", 4)
	}
	var document Document
	if len(object.Data) > MaxSize || json.Unmarshal(object.Data, &document) != nil || document.Version != 1 || document.ID == "" || !json.Valid(document.Data) || object.Version == "" || document.Lock != nil && document.Lock.ID == "" {
		return Document{}, "", problem.New("invalid_remote_state", "The durable state document is invalid. Restore a known-good object version before continuing.", 4)
	}
	return document, object.Version, nil
}

func Initialize(ctx context.Context, backend Backend, data json.RawMessage) (Document, error) {
	document := Document{Version: 1, ID: rand.Text(), Data: data}
	encoded, err := json.Marshal(document)
	if err != nil {
		return Document{}, Unavailable()
	}
	if _, err := backend.Write(ctx, encoded, ""); err != nil {
		return Document{}, err
	}
	return document, nil
}

func Acquire(ctx context.Context, backend Backend) (*Session, error) {
	document, version, err := Read(ctx, backend)
	if err != nil {
		return nil, err
	}
	if document.Lock != nil {
		return nil, problem.New("state_locked", fmt.Sprintf("Durable state is locked by %q since %s. Lock ID: %s. Wait for that deployment, or use zorveus state unlock only after confirming it has stopped.", document.Lock.Owner, document.Lock.CreatedAt.Format(time.RFC3339), document.Lock.ID), 4)
	}
	owner, _ := os.Hostname()
	document.Lock = &Lock{ID: rand.Text(), Owner: owner, CreatedAt: time.Now().UTC()}
	session := &Session{backend: backend, Document: document, version: version}
	if err := session.write(ctx); err != nil {
		return nil, err
	}
	return session, nil
}

func (session *Session) Save(ctx context.Context, data json.RawMessage) error {
	if session.failed || session.Document.Lock == nil {
		return Conflict()
	}
	session.Document.Data = data
	session.Document.Revision++
	return session.write(ctx)
}

func (session *Session) Close() error {
	if session.failed {
		return problem.New("state_lock_retained", "Durable state could not be confirmed. Its lock was retained. Inspect state before unlocking or resuming this deployment.", 5)
	}
	// Cancellation stops API work, but a healthy owner can still release its lock.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session.Document.Lock = nil
	return session.write(ctx)
}

func (session *Session) write(ctx context.Context) error {
	data, err := json.Marshal(session.Document)
	if err != nil {
		return Unavailable()
	}
	version, err := session.backend.Write(ctx, data, session.version)
	if err == nil && version == "" {
		err = Unavailable()
	}
	if err != nil {
		session.failed = true
		return err
	}
	session.version = version
	return nil
}

func Unlock(ctx context.Context, backend Backend, lockID string) error {
	document, version, err := Read(ctx, backend)
	if err != nil {
		return err
	}
	if document.Lock == nil || lockID == "" || document.Lock.ID != lockID {
		return problem.New("lock_id_mismatch", "The supplied lock ID does not match the current durable-state lock. Inspect state before continuing.", 4)
	}
	document.Lock = nil
	data, err := json.Marshal(document)
	if err != nil {
		return Unavailable()
	}
	_, err = backend.Write(ctx, data, version)
	return err
}
