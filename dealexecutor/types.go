package dealexecutor

import "github.com/ipfs/go-cid"

import "time"

// DealTrigger inspect existing deals that satisfy certain policy
type DealTrigger interface {
	// Listen returns deals that satisfy the inspection policy
	Listen() (<-chan cid.Cid, error)
	// Unregister a channel from the notification hub
	Unregister(n <-chan cid.Cid) error
	// Closes the trigger
	Close() error
}

// DealRedoer re-executes a particular deal again in the network
type DealRedoer interface {
	// Redo triggers a redo in an existing deal in the network
	Redo(deal cid.Cid) (cid.Cid, error)
}

type LogEntry interface {
	IsError() bool
	CidTrigger() cid.Cid
	Timestamp() time.Time
	// CreatedCid might be nil if IsError() is true
	CreatedCid() cid.Cid
}
