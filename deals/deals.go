package deals

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/lotus/api"
	str "github.com/filecoin-project/lotus/chain/store"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log"
)

const (
	initialWait      = time.Second * 5
	chanWriteTimeout = time.Second
)

var (
	log = logging.Logger("deals")
)

// Module exposes storage and monitoring from the market.
type Module struct {
	api API
	cfg *Config
}

// API interacts with a Filecoin full-node
type API interface {
	ClientStartDeal(ctx context.Context, data cid.Cid, addr address.Address, miner address.Address, price types.BigInt, blocksDuration uint64) (*cid.Cid, error)
	ClientImport(ctx context.Context, path string) (cid.Cid, error)
	ClientGetDealInfo(context.Context, cid.Cid) (*api.DealInfo, error)
	ChainNotify(ctx context.Context) (<-chan []*str.HeadChange, error)
}

// New creates a new deal module
func New(api API, opts ...Option) (*Module, error) {
	var cfg Config
	for _, o := range opts {
		if err := o(&cfg); err != nil {
			return nil, err
		}
	}
	if cfg.ImportPath == "" {
		return nil, fmt.Errorf("import path can't be empty")
	}
	return &Module{
		api: api,
		cfg: &cfg,
	}, nil
}

// Store creates a proposal deal for data using wallet addr to all miners indicated
// by dealConfigs for duration epochs
func (m *Module) Store(ctx context.Context, waddr string, data io.Reader, dcfgs []DealConfig, dur uint64) ([]cid.Cid, []DealConfig, error) {
	f, err := ioutil.TempFile(m.cfg.ImportPath, "import-*")
	if err != nil {
		return nil, nil, fmt.Errorf("error when creating tmpfile: %s", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, data); err != nil {
		return nil, nil, fmt.Errorf("error when copying data to tmpfile: %s", err)
	}
	dataCid, err := m.api.ClientImport(ctx, f.Name())
	if err != nil {
		return nil, nil, fmt.Errorf("error when importing data: %s", err)
	}
	addr, err := address.NewFromString(waddr)
	if err != nil {
		return nil, nil, err
	}

	var proposals []cid.Cid
	var failed []DealConfig
	for _, c := range dcfgs {
		maddr, err := address.NewFromString(c.Miner)
		if err != nil {
			log.Errorf("invalid miner address %v: %s", c, err)
			failed = append(failed, c)
			continue
		}
		proposal, err := m.api.ClientStartDeal(ctx, dataCid, addr, maddr, c.EpochPrice, dur)
		if err != nil {
			log.Errorf("starting deal with %v: %s", c, err)
			failed = append(failed, c)
			continue
		}
		proposals = append(proposals, *proposal)
	}
	return proposals, failed, nil
}

// Watch returnas a channel with state changes of indicated proposals
func (m *Module) Watch(ctx context.Context, proposals []cid.Cid) (<-chan DealInfo, error) {
	ch := make(chan DealInfo)
	w, err := m.api.ChainNotify(ctx)
	if err != nil {
		return nil, fmt.Errorf("error when listening to chain changes: %s", err)
	}
	go func() {
		defer close(ch)
		currentState := make(map[cid.Cid]*api.DealInfo)
		for {
			select {
			case <-ctx.Done():
				return
			case <-w:
				if err := m.pushNewChanges(ctx, currentState, proposals, ch); err != nil {
					log.Errorf("error when pushing new proposal states: %s", err)
				}
			}
		}
	}()
	return ch, nil
}

func (m *Module) pushNewChanges(ctx context.Context, currState map[cid.Cid]*api.DealInfo, proposals []cid.Cid, ch chan<- DealInfo) error {
	for _, pcid := range proposals {
		dinfo, err := m.api.ClientGetDealInfo(ctx, pcid)
		if err != nil {
			log.Errorf("error when getting deal proposal info %s: %s", pcid, err)
			continue
		}
		if currState[pcid] == nil || (*currState[pcid]).State != dinfo.State {
			currState[pcid] = dinfo
			newState := DealInfo{
				ProposalCid:   dinfo.ProposalCid,
				StateID:       dinfo.State,
				StateName:     api.DealStates[dinfo.State],
				Miner:         dinfo.Provider.String(),
				PieceRef:      dinfo.PieceRef,
				Size:          dinfo.Size,
				PricePerEpoch: dinfo.PricePerEpoch,
				Duration:      dinfo.Duration,
			}
			select {
			case <-ctx.Done():
				return nil
			case ch <- newState:
			case <-time.After(chanWriteTimeout):
				log.Warnf("dropping new state since chan is blocked")
			}
		}
	}
	return nil
}
