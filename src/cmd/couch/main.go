// Command couch serves an Emby-compatible API, Emby Web assets, and local media.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/TokenCemetery/couch/internal/hls"
	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/procusage"
	"github.com/TokenCemetery/couch/internal/server"
	"github.com/TokenCemetery/couch/internal/state"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	data := flag.String("data", "./data", "private state directory")
	listen := flag.String("listen", "127.0.0.1:8097", "HTTP listen address")
	name := flag.String("name", "Couch", "server display name")
	upstream := flag.String("web-upstream", "", "optional Emby origin supplying only /web static assets")
	webDir := flag.String("web-dir", "", "optional local Emby Web directory containing index.html")
	mediaDir := flag.String("media-dir", "", "optional read-only movie/series directory; scanned with ffprobe at startup and on SIGHUP, indexed in -data")
	rescanInterval := flag.Duration("rescan-interval", 0, "rescan -media-dir this often, for example 30m; 0 disables")
	init := flag.Bool("init", false, "initialize a local user; read password from stdin and exit")
	username := flag.String("username", "", "username for -init")
	ffmpegThreads := flag.Int("ffmpeg-threads", 0, "cap FFmpeg decoder and encoder threads per transcode; 0 follows a CPU quota, or leaves them to FFmpeg without one")
	hwaccel := flag.String("hwaccel", "", "encode transcodes on hardware: "+strings.Join(hls.Accels, ", ")+"; empty uses libx264")
	hwaccelDevice := flag.String("hwaccel-device", "/dev/dri/renderD128", "DRM render node for -hwaccel vaapi and qsv")
	flag.Parse()
	if *webDir != "" && *upstream != "" {
		return errors.New("choose either -web-dir or -web-upstream")
	}
	if *rescanInterval < 0 || (*rescanInterval > 0 && *rescanInterval < time.Minute) {
		return errors.New("-rescan-interval must be 0 or at least 1m")
	}
	if *ffmpegThreads < 0 {
		return errors.New("-ffmpeg-threads must be 0 or a positive integer")
	}
	if *hwaccel != "" && !slices.Contains(hls.Accels, *hwaccel) {
		return fmt.Errorf("-hwaccel must be one of %s", strings.Join(hls.Accels, ", "))
	}
	s, err := state.Open(*data)
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	defer func() { _ = s.Close() }()
	if *init {
		if s.Initialized() {
			return errors.New("already initialized; existing state was not modified")
		}
		if *username == "" {
			return errors.New("-init requires -username")
		}
		password, err := state.ReadPassword(os.Stdin)
		if err != nil {
			return err
		}
		if err := s.Initialize(*username, password); err != nil {
			return err
		}
		fmt.Println("Local user initialized.")
		return nil
	}
	if !s.Initialized() {
		return errors.New("initialize first with -init -username NAME and a password on stdin")
	}
	var web http.Handler
	if *webDir != "" {
		local, err := server.OpenWebDirectory(*webDir)
		if err != nil {
			return err
		}
		defer func() { _ = local.Close() }()
		web = local
	}
	if *upstream != "" {
		web, err = server.WebAssets(*upstream)
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var catalog *media.Catalog
	catalogFile := filepath.Join(*data, "catalog.json")
	loaded := false
	if *mediaDir != "" {
		// A saved index serves at once; the rescan below catches up with
		// changes made while Couch was stopped.
		catalog, err = media.Load(ctx, *mediaDir, catalogFile)
		switch {
		case err == nil:
			loaded = true
			slog.Info("Media index loaded; rescanning in the background", "videos", len(catalog.Items), "folders", len(catalog.Folders))
		case errors.Is(err, fs.ErrNotExist):
		default:
			slog.Warn("Media index not used; scanning", "error", err)
		}
		if !loaded {
			slog.Info("Scanning media directory")
			scanCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			catalog, err = media.Scan(scanCtx, *mediaDir)
			cancel()
			if err != nil {
				return fmt.Errorf("scan media: %w", err)
			}
			slog.Info("Media scan complete", "videos", len(catalog.Items), "folders", len(catalog.Folders), "skipped", catalog.Skipped,
				"probes", catalog.Probes.Runs, "probe_cpu", catalog.Probes.CPU.Round(time.Millisecond), "probe_peak_rss_kib", catalog.Probes.PeakKiB)
			saveCatalog(catalog, catalogFile)
		}
		defer func() { _ = catalog.Close() }()
	}
	api := server.New(s, *name, web, catalog)
	defer api.Close()
	if catalog != nil {
		ffmpeg, ffmpegErr := exec.LookPath("ffmpeg")
		ffprobe, ffprobeErr := exec.LookPath("ffprobe")
		if ffmpegErr == nil && ffprobeErr == nil {
			// Deferred after api.Close, so it runs before it: after the HTTP
			// server has shut down, jobs are killed and segments removed.
			remux, err := hls.NewManager(ffmpeg, ffprobe, filepath.Join(*data, "transcode"))
			if err != nil {
				return fmt.Errorf("prepare HLS directory: %w", err)
			}
			defer remux.Close()
			remux.Threads = *ffmpegThreads
			// Go sizes GOMAXPROCS from a cgroup CPU quota; FFmpeg sizes its
			// threads from the host's CPUs, which under a quota delays the
			// first segment and doubles memory (#92, #99).
			if remux.Threads == 0 && runtime.GOMAXPROCS(0) < runtime.NumCPU() {
				remux.Threads = runtime.GOMAXPROCS(0)
				slog.Info("FFmpeg transcode threads follow the CPU quota", "threads", remux.Threads)
			}
			if *hwaccel != "" {
				remux.Accel = hls.Accel{Kind: *hwaccel, Device: *hwaccelDevice}
				if err := remux.CheckAccel(context.Background()); err != nil {
					return fmt.Errorf("-hwaccel %s: %w", *hwaccel, err)
				}
			}
			api.SetHLS(remux)
		} else if *hwaccel != "" {
			return errors.New("-hwaccel requires ffmpeg and ffprobe")
		} else {
			slog.Info("FFmpeg not found; HLS remux is disabled")
		}
	}
	if catalog != nil {
		// Registered before serving: an unhandled SIGHUP would stop Couch.
		hangup := make(chan os.Signal, 1)
		signal.Notify(hangup, syscall.SIGHUP)
		defer signal.Stop(hangup)
		publish := func(next *media.Catalog) {
			api.SetCatalog(next)
			saveCatalog(next, catalogFile)
		}
		go rescanLoop(ctx, catalog, hangup, *rescanInterval, loaded, publish)
	}
	httpServer := &http.Server{Addr: *listen, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	slog.Info("Couch starting", "address", *listen, "version", server.Version)
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		api.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
			return err
		}
		logUsage()
		return nil
	}
}

// logUsage records what Couch itself and, separately, its finished FFmpeg
// and FFprobe runs cost over the process's life.
func logUsage() {
	cpu, peak, err := procusage.Self(syscall.RUSAGE_SELF)
	childCPU, childPeak, childErr := procusage.Self(syscall.RUSAGE_CHILDREN)
	if err != nil || childErr != nil {
		return
	}
	slog.Info("Couch stopped", "cpu", cpu.Round(time.Millisecond), "peak_rss_kib", peak,
		"children_cpu", childCPU.Round(time.Millisecond), "children_peak_rss_kib", childPeak)
}

// saveCatalog writes the media index; a failure costs only a full scan at
// the next start.
func saveCatalog(catalog *media.Catalog, file string) {
	if err := catalog.Save(file); err != nil {
		slog.Warn("Media index not saved", "error", err)
	}
}

// rescanLoop rescans on SIGHUP and every interval (0 disables), one scan at a
// time: a signal during a scan starts one more scan after it, and ticks during
// a scan are dropped. With now set it rescans once right away. A failed scan
// keeps the published catalog.
func rescanLoop(ctx context.Context, catalog *media.Catalog, hangup <-chan os.Signal, interval time.Duration, now bool, publish func(*media.Catalog)) {
	var tick <-chan time.Time
	if interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		if !now {
			select {
			case <-ctx.Done():
				return
			case <-hangup:
			case <-tick:
			}
		}
		now = false
		slog.Info("Rescanning media directory")
		scanCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		next, err := catalog.Rescan(scanCtx)
		cancel()
		if err != nil {
			slog.Warn("Media rescan failed; keeping the previous catalog", "error", err)
			continue
		}
		catalog = next
		publish(catalog)
		slog.Info("Media rescan complete", "videos", len(catalog.Items), "folders", len(catalog.Folders), "skipped", catalog.Skipped,
			"probes", catalog.Probes.Runs, "probe_cpu", catalog.Probes.CPU.Round(time.Millisecond), "probe_peak_rss_kib", catalog.Probes.PeakKiB)
	}
}
