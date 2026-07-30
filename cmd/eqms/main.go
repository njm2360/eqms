package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/njm2360/eqms/internal/core"
	"github.com/njm2360/eqms/internal/source"
	"github.com/njm2360/eqms/internal/store"
	"github.com/njm2360/eqms/internal/web"
)

func main() {
	var (
		listen     = envStr("EQMS_LISTEN", "127.0.0.1:8080")
		serialPort = envStr("EQMS_SERIAL_PORT", "")
		baud       = envInt("EQMS_SERIAL_BAUD", 0)
		dbPath     = envStr("EQMS_DB", "eqms.db")
		sim        = envBool("EQMS_SIM")
		silence    = envDuration("EQMS_SERIAL_SILENCE", source.DefaultSilence)
		webCfg     = web.Config{
			MaxStreams:  envInt("EQMS_MAX_STREAMS", 100),
			StreamWrite: envDuration("EQMS_STREAM_WRITE", web.DefaultStreamWrite),
		}
		cfg = core.Config{
			Retention:      envDuration("EQMS_RETENTION", 720*time.Hour),
			StartIntensity: envFloat("EQMS_START_INTENSITY", 0),
			EndIntensity:   envFloat("EQMS_END_INTENSITY", 0),
			PreBuffer:      envDuration("EQMS_PREBUFFER", 0),
			EndQuiet:       envDuration("EQMS_END_QUIET", 0),
			MaxEvent:       envDuration("EQMS_MAX_EVENT", 0),
		}
	)

	if !sim {
		if serialPort == "" {
			log.Fatal("EQMS_SERIAL_PORT is required (ex: /dev/ttyACM0)")
		}
		if baud <= 0 {
			log.Fatal("EQMS_SERIAL_BAUD is required (ex: 115200)")
		}
	}
	if cfg.PreBuffer > core.MaxPreBuffer {
		log.Fatalf("EQMS_PREBUFFER: must be <= %s", core.MaxPreBuffer)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	writer := store.NewWriter(st)

	engine, err := core.NewEngine(st, writer, cfg)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}
	events := make(chan source.Event, 256)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if sim {
			log.Printf("running in simulator mode")
			source.RunSim(ctx, events)
		} else {
			source.RunSerial(ctx, serialPort, baud, silence, events)
		}
	}()

	engineDone := make(chan struct{})
	go func() {
		engine.Run(ctx, events)
		close(engineDone)
	}()

	srv := &http.Server{
		Addr:              listen,
		Handler:           web.NewServer(engine, st, webCfg).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		// WriteTimeout は SSE を切ってしまうので設定しない
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("eqms: listening on %s", listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http: %v", err)
	}
	<-engineDone   // 進行中の記録をクローズさせる
	writer.Close() // 予約済みの書き込みを流し切る
	st.Close()
}
