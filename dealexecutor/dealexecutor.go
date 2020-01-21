package dealexecutor

import (
	"time"

	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log"
)

var (
	log = logging.Logger("deal-executor")
)

// DealExecutor listens for a trigger
type DealExecutor struct {
	dt DealTrigger
	dr DealRedoer
}

func New(dt DealTrigger, dr DealRedoer) (*DealExecutor, error) {
	de := &DealExecutor{
		dt: dt,
		dr: dr,
	}
	return de, nil
}

// Start starts listening to the trigger and executing redos of them
func (de *DealExecutor) Start(sinceHeight int) error {
	// ToDo
	// Sketch:

	dls, err := de.dt.Listen()
	if err != nil {
		return err
	}

	for triggered := range dls {
		new, err := de.dr.Redo(triggered)
		if err != nil {
			if err := de.addErrorEvent(triggered, err); err != nil {
				log.Errorf("error when adding event: %s", err)
			}
			log.Errorf("error while registering error for %s: %s", triggered, err)
			continue
		}
		if err := de.addSuccessEvent(triggered, new); err != nil {
			log.Errorf("error while registering success for %s, %s: %s", triggered, new, err)
		}
	}
	log.Info("deal triggered closed")

	return nil
}

// GetLog returns the log of events of the executor. If there're limit number of
// items in the returned slice, may be a sign that there're more to fetch.
func (de *DealExecutor) GetLog(since time.Time, limit int) ([]LogEntry, error) {
	panic("TODO")
}

// addErrorEvent adds an error happened when calling redoer for a triggered cid
func (de *DealExecutor) addErrorEvent(c cid.Cid, err error) error {
	panic("TODO")
}

// addSuccessEvent adds returned new cid of a triggered cid
func (de *DealExecutor) addSuccessEvent(c cid.Cid, cid cid.Cid) error {
	panic("TODO")
}
