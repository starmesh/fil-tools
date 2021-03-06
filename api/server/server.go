package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/ipfs/go-datastore"
	badger "github.com/ipfs/go-ds-badger2"
	logging "github.com/ipfs/go-log"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/textileio/fil-tools/deals"
	dealsPb "github.com/textileio/fil-tools/deals/pb"
	"github.com/textileio/fil-tools/fchost"
	"github.com/textileio/fil-tools/index/ask"
	askPb "github.com/textileio/fil-tools/index/ask/pb"
	"github.com/textileio/fil-tools/index/miner"
	minerPb "github.com/textileio/fil-tools/index/miner/pb"
	"github.com/textileio/fil-tools/index/slashing"
	slashingPb "github.com/textileio/fil-tools/index/slashing/pb"
	"github.com/textileio/fil-tools/iplocation/ip2location"
	"github.com/textileio/fil-tools/lotus"
	"github.com/textileio/fil-tools/reputation"
	reputationPb "github.com/textileio/fil-tools/reputation/pb"
	txndstr "github.com/textileio/fil-tools/txndstransform"
	"github.com/textileio/fil-tools/wallet"
	walletPb "github.com/textileio/fil-tools/wallet/pb"
	"google.golang.org/grpc"
)

const (
	datastoreFolderName = "datastore"
)

var (
	log = logging.Logger("server")
)

// Server represents the configured lotus client and filecoin grpc server
type Server struct {
	ds datastore.TxnDatastore

	ip2l *ip2location.IP2Location

	ai *ask.AskIndex
	mi *miner.MinerIndex
	si *slashing.SlashingIndex
	dm *deals.Module
	wm *wallet.Module
	rm *reputation.Module

	dealsService      *deals.Service
	walletService     *wallet.Service
	reputationService *reputation.Service
	askService        *ask.Service
	minerService      *miner.Service
	slashingService   *slashing.Service

	grpcServer   *grpc.Server
	grpcWebProxy *http.Server

	closeLotus func()
}

// Config specifies server settings.
type Config struct {
	LotusAddress        ma.Multiaddr
	LotusAuthToken      string
	GrpcHostNetwork     string
	GrpcHostAddress     string
	GrpcServerOpts      []grpc.ServerOption
	GrpcWebProxyAddress string
	RepoPath            string
}

// NewServer starts and returns a new server with the given configuration.
func NewServer(conf Config) (*Server, error) {
	c, cls, err := lotus.New(conf.LotusAddress, conf.LotusAuthToken)
	if err != nil {
		return nil, err
	}

	fchost, err := fchost.New()
	if err != nil {
		return nil, fmt.Errorf("error when creating filecoin host: %s", err)
	}
	if err := fchost.Bootstrap(); err != nil {
		return nil, fmt.Errorf("error when bootstrapping filecoin host: %s", err)
	}

	path := filepath.Join(conf.RepoPath, datastoreFolderName)
	if err := os.MkdirAll(path, os.ModePerm); err != nil {
		return nil, fmt.Errorf("error when creating repo folder: %s", err)
	}

	ds, err := badger.NewDatastore(path, &badger.DefaultOptions)
	if err != nil {
		return nil, fmt.Errorf("error when opening datastore on repo: %s", err)
	}
	ip2l := ip2location.New([]string{"./ip2location-ip4.bin"})

	ai, err := ask.New(txndstr.Wrap(ds, "index/ask"), c)
	if err != nil {
		return nil, fmt.Errorf("error when creating ask index: %s", err)
	}
	mi, err := miner.New(txndstr.Wrap(ds, "index/miner"), c, fchost, ip2l)
	if err != nil {
		return nil, fmt.Errorf("error when creating miner index: %s", err)
	}
	si, err := slashing.New(txndstr.Wrap(ds, "index/slashing"), c)
	if err != nil {
		return nil, fmt.Errorf("error when creating slashing index: %s", err)
	}
	dm, err := deals.New(c, deals.WithImportPath(filepath.Join(conf.RepoPath, "imports")))
	if err != nil {
		return nil, fmt.Errorf("error when creating deal module: %s", err)
	}
	wm := wallet.New(c)
	rm := reputation.New(txndstr.Wrap(ds, "reputation"), mi, si, ai)

	dealsService := deals.NewService(dm)
	walletService := wallet.NewService(wm)
	reputationService := reputation.NewService(rm)
	askService := ask.NewService(ai)
	minerService := miner.NewService(mi)
	slashingService := slashing.NewService(si)

	grpcServer := grpc.NewServer(conf.GrpcServerOpts...)

	wrappedServer := grpcweb.WrapServer(
		grpcServer,
		grpcweb.WithOriginFunc(func(origin string) bool {
			return true
		}),
		grpcweb.WithWebsockets(true),
		grpcweb.WithWebsocketOriginFunc(func(req *http.Request) bool {
			return true
		}),
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wrappedServer.IsGrpcWebRequest(r) ||
			wrappedServer.IsAcceptableGrpcCorsRequest(r) ||
			wrappedServer.IsGrpcWebSocketRequest(r) {
			wrappedServer.ServeHTTP(w, r)
		}
	})
	grpcWebProxy := &http.Server{
		Addr:    conf.GrpcWebProxyAddress,
		Handler: handler,
	}

	s := &Server{
		ds: ds,

		ip2l: ip2l,

		ai: ai,
		mi: mi,
		si: si,
		dm: dm,
		wm: wm,
		rm: rm,

		dealsService:      dealsService,
		walletService:     walletService,
		reputationService: reputationService,
		askService:        askService,
		minerService:      minerService,
		slashingService:   slashingService,

		grpcServer:   grpcServer,
		grpcWebProxy: grpcWebProxy,

		closeLotus: cls,
	}

	listener, err := net.Listen(conf.GrpcHostNetwork, conf.GrpcHostAddress)
	if err != nil {
		return nil, fmt.Errorf("error when listening to grpc: %s", err)
	}
	go func() {
		dealsPb.RegisterAPIServer(grpcServer, s.dealsService)
		walletPb.RegisterAPIServer(grpcServer, s.walletService)
		reputationPb.RegisterAPIServer(grpcServer, s.reputationService)
		askPb.RegisterAPIServer(grpcServer, s.askService)
		minerPb.RegisterAPIServer(grpcServer, s.minerService)
		slashingPb.RegisterAPIServer(grpcServer, s.slashingService)
		grpcServer.Serve(listener)
	}()

	go func() {
		grpcWebProxy.ListenAndServe()
	}()

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/index/ask", func(w http.ResponseWriter, r *http.Request) {
			index := ai.Get()
			buf, err := json.MarshalIndent(index, "", "  ")
			if err != nil {
				http.Error(w, "Error", http.StatusInternalServerError)
				return
			}
			w.Write(buf)
		})
		mux.HandleFunc("/index/miners", func(w http.ResponseWriter, r *http.Request) {
			index := mi.Get()
			buf, err := json.MarshalIndent(index, "", "  ")
			if err != nil {
				http.Error(w, "Error", http.StatusInternalServerError)
				return
			}
			w.Write(buf)
		})
		mux.HandleFunc("/index/slashing", func(w http.ResponseWriter, r *http.Request) {
			index := si.Get()
			buf, err := json.MarshalIndent(index, "", "  ")
			if err != nil {
				http.Error(w, "Error", http.StatusInternalServerError)
				return
			}
			w.Write(buf)
		})
		if err := http.ListenAndServe(":8889", mux); err != nil {
			log.Fatalf("Failed to run Prometheus scrape endpoint: %v", err)
		}
	}()

	return s, nil
}

// Close shuts down the server
func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := s.grpcWebProxy.Shutdown(ctx); err != nil {
		log.Errorf("error shutting down proxy: %s", err)
	}
	s.grpcServer.GracefulStop()
	if err := s.ai.Close(); err != nil {
		log.Errorf("error when closing ask index: %s", err)
	}
	if err := s.mi.Close(); err != nil {
		log.Errorf("error when closing miner index: %s", err)
	}
	if err := s.si.Close(); err != nil {
		log.Errorf("error when closing slashing index: %s", err)
	}
	if err := s.ds.Close(); err != nil {
		log.Errorf("error when closing datastore: %s", err)
	}
	s.closeLotus()
	s.ip2l.Close()
}
