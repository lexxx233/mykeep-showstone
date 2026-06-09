// Command showstone is a portable, USB-resident browser that an AI agent drives over a
// local REST API. Launch with no args (or `gui`) to open the browser dashboard; `serve`
// is the headless equivalent (password via env/stdin).
//
//	showstone          open the GUI dashboard (default)
//	showstone serve    headless: unlock at launch, serve the REST API only
//	showstone guide    print the agent operating manual
//	showstone version  print version
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	showstone "mykeep.ai/showstone/component"
	"mykeep.ai/showstone/internal/config"
	"mykeep.ai/showstone/internal/gui"
	"mykeep.ai/showstone/internal/paths"
	"mykeep.ai/showstone/internal/secret"
	"mykeep.ai/showstone/internal/server"
)

var version = "0.1.0-dev"

const addr = "127.0.0.1:8771"

func main() {
	cmd := "gui"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "gui":
		err = cmdGUI()
	case "serve":
		err = cmdServe()
	case "guide":
		fmt.Println(server.GuideText(addr))
	case "version":
		fmt.Printf("showstone %s (chrome-for-testing %s)\n", version, browserVersion())
	default:
		fmt.Fprintln(os.Stderr, "usage: showstone <gui|serve|guide|version>")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "showstone: "+err.Error())
		os.Exit(1)
	}
}

func cmdGUI() error {
	layout, err := paths.Resolve()
	if err != nil {
		return err
	}
	if !layout.Portable {
		fmt.Fprintln(os.Stderr, "warning: running non-portable — the profile is on the host, not the stick")
	}
	app, err := gui.New(layout, version, addr, 15*time.Minute)
	if err != nil {
		return err
	}
	return app.Run()
}

func cmdServe() error {
	ctx := context.Background()
	layout, err := paths.Resolve()
	if err != nil {
		return err
	}
	comp, err := showstone.New(showstone.Options{
		DataDir: layout.DataDir, Portable: layout.Portable, Version: version,
		Headless:  os.Getenv("SHOWSTONE_HEADLESS") != "0", // serve defaults headless (server context)
		NoSandbox: os.Getenv("SHOWSTONE_NO_SANDBOX") == "1",
		Addr:      addr,
		OnDownload: func(p int) { fmt.Fprintf(os.Stderr, "\rdownloading browser engine… %d%%   ", p) },
	})
	if err != nil {
		return err
	}

	dek, err := unlockDEK(layout.ConfigPath())
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "launching browser…")
	if err := comp.Unlock(ctx, dek); err != nil {
		return err
	}

	mux := http.NewServeMux()
	comp.Mount(mux)
	httpSrv := &http.Server{Addr: addr, Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		if e := httpSrv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			errCh <- e
		}
	}()

	fmt.Printf("\n🔮 Showstone serving on http://%s\n", addr)
	fmt.Printf("  agent (use)  token: %s\n", comp.UseToken())
	fmt.Printf("  GUI (control) token: %s   ← keep this off the wire\n", comp.ControlToken())
	fmt.Printf("  the agent fetches its manual at  GET http://%s/v1/showstone/guide\n\n", addr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
		fmt.Fprintln(os.Stderr, "\nshutting down: sealing the browser profile…")
	case e := <-errCh:
		_ = comp.Lock()
		return e
	}
	shutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	return comp.Lock()
}

func unlockDEK(cfgPath string) ([]byte, error) {
	pw := passphrase()
	if len(pw) == 0 {
		return nil, errors.New("empty passphrase")
	}
	defer wipe(pw)
	if config.Exists(cfgPath) {
		c, err := config.Load(cfgPath)
		if err != nil {
			return nil, err
		}
		dek, err := c.Secret.Unwrap(pw)
		if err != nil {
			return nil, errors.New("wrong password")
		}
		return dek, nil
	}
	env, dek, err := secret.NewEnvelope(pw)
	if err != nil {
		return nil, err
	}
	c := config.Default()
	c.Secret = env
	if err := config.Save(cfgPath, &c); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "• created a new Showstone profile")
	return dek, nil
}

func passphrase() []byte {
	if v := os.Getenv("SHOWSTONE_PASSPHRASE"); v != "" {
		return []byte(v)
	}
	fmt.Fprint(os.Stderr, "Showstone passphrase: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return []byte(strings.TrimRight(line, "\r\n"))
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func browserVersion() string { return showstone.ChromeVersion }
