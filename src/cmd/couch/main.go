// Command couch serves an Emby-compatible API, Emby Web assets, and local media.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
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
	"github.com/TokenCemetery/couch/internal/library"
	"github.com/TokenCemetery/couch/internal/logfile"
	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
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
	mediaDir := flag.String("media-dir", "", "read-only directory whose Movies and TV Shows libraries are created on the first start; libraries are then managed in Emby Web")
	var mediaRoots []string
	flag.Func("media-root", "folder below which an administrator may add library folders in Emby Web; repeatable; defaults to -media-dir", func(v string) error {
		mediaRoots = append(mediaRoots, v)
		return nil
	})
	rescanInterval := flag.Duration("rescan-interval", 0, "rescan the libraries this often, for example 30m; 0 disables")
	init := flag.Bool("init", false, "initialize a local user; read password from stdin and exit")
	username := flag.String("username", "", "username for -init")
	backup := flag.String("backup", "", "copy the state in -data to this new or empty directory and exit; works while the server runs")
	ffmpegThreads := flag.Int("ffmpeg-threads", 0, "cap FFmpeg decoder and encoder threads per transcode; 0 follows a CPU quota, or leaves them to FFmpeg without one")
	hwaccel := flag.String("hwaccel", "", "encode transcodes on hardware: "+strings.Join(hls.Accels, ", ")+"; empty uses libx264")
	hwaccelDevice := flag.String("hwaccel-device", "/dev/dri/renderD128", "DRM render node for -hwaccel vaapi and qsv")
	toneMap := flag.String("tonemap", "auto", "tone-map transcoded HDR video to SDR: auto (on the GPU with libplacebo when Vulkan works), software (with zscale on the CPU when it does not), off")
	metadataLanguage := flag.String("metadata-language", "en", "language of online metadata: "+strings.Join(metadata.SupportedLanguages(), ", "))
	metadataRefresh := flag.Duration("metadata-refresh", 0, "fetch online metadata again once it is this old, for example 720h; 0 never does")
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
	if *metadataRefresh < 0 || (*metadataRefresh > 0 && *metadataRefresh < time.Hour) {
		return errors.New("-metadata-refresh must be 0 or at least 1h")
	}
	if !slices.Contains(metadata.SupportedLanguages(), *metadataLanguage) {
		return fmt.Errorf("-metadata-language must be one of %s", strings.Join(metadata.SupportedLanguages(), ", "))
	}
	if !slices.Contains(hls.ToneMapModes, *toneMap) {
		return errors.New("-tonemap must be auto, software or off")
	}
	if *hwaccel != "" && !slices.Contains(hls.Accels, *hwaccel) {
		return fmt.Errorf("-hwaccel must be one of %s", strings.Join(hls.Accels, ", "))
	}
	if *backup != "" {
		if err := state.Backup(*data, *backup); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		fmt.Println("Backup written.")
		return nil
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
	// The log also goes to files in -data, which the dashboard's logs page
	// shows; standard error keeps it for the service manager.
	logDir := filepath.Join(*data, "logs")
	logFile, err := logfile.Open(logDir)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	log.SetOutput(io.MultiWriter(os.Stderr, logFile))
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
	if len(mediaRoots) == 0 && *mediaDir != "" {
		mediaRoots = []string{*mediaDir}
	}
	api := server.New(s, *name, web, nil)
	defer api.Close()
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
		if err := remux.SetToneMap(context.Background(), *toneMap); err != nil {
			return fmt.Errorf("-tonemap %s: %w", *toneMap, err)
		}
		api.SetHLS(remux)
	} else if *hwaccel != "" {
		return errors.New("-hwaccel requires ffmpeg and ffprobe")
	} else {
		slog.Info("FFmpeg not found; HLS remux is disabled")
	}
	// An online provider is used only when its key is set (#101).
	config := metadata.Config{TMDbKey: os.Getenv("COUCH_TMDB_API_KEY"), TVDbKey: os.Getenv("COUCH_TVDB_API_KEY"),
		Language: *metadataLanguage, Refresh: *metadataRefresh, ImageDir: filepath.Join(*data, "metadata")}
	refresher, err := metadata.NewRefresher(s, config, api.SetMetadata)
	if err != nil {
		return err
	}
	api.SetMetadataImages(config.ImageDir)
	api.SetCoverCache(filepath.Join(*data, "covers"))
	api.SetRefresher(refresher)
	api.SetLogDir(logDir)
	slog.Info("Metadata sources", "nfo", true, "tmdb", config.TMDbKey != "", "tvdb", config.TVDbKey != "", "language", config.Language)
	libraries, err := library.New(s, mediaRoots, filepath.Join(*data, "catalog.json"), func(next *media.Catalog) {
		api.SetCatalog(next)
		refresher.Update(next)
	})
	if err != nil {
		return err
	}
	// Deferred first, so it runs last: after the refresher stopped reading
	// sidecars and the HTTP server closed its streams.
	defer libraries.Close()
	api.SetLibraries(libraries)
	loaded, err := libraries.Open(ctx, *mediaDir)
	if err != nil {
		return fmt.Errorf("scan media: %w", err)
	}
	refreshCtx, stopRefresh := context.WithCancel(ctx)
	refreshed := make(chan struct{})
	go func() {
		defer close(refreshed)
		refresher.Run(refreshCtx)
	}()
	// Stops the refresher before the deferred libraries.Close.
	defer func() {
		stopRefresh()
		<-refreshed
	}()
	// Registered before serving: an unhandled SIGHUP would stop Couch.
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	defer signal.Stop(hangup)
	go libraries.Run(ctx, hangup, *rescanInterval, loaded)
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
