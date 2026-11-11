package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/net/netutil"
	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil {
		log.Print("ROS Watch stopped: ", err)
		os.Exit(1)
	}
}

func run() error {
	config, err := loadConfig()
	if err != nil {
		return err
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
			response, err := client.Get("http://127.0.0.1:" + strconv.Itoa(config.HTTPPort) + "/healthz")
			if err != nil {
				return errors.New("healthcheck failed")
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			if response.StatusCode != 200 {
				return errors.New("service degraded")
			}
			return nil
		case "reset-password":
			return resetCLI(config, os.Args[2:])
		case "version":
			fmt.Println("ROS Watch", Version, "(Go)")
			return nil
		default:
			return errors.New("usage: ros-watch [version|healthcheck|reset-password [--username NAME]]")
		}
	}
	app, err := newApp(config)
	if err != nil {
		return fmt.Errorf("initialization failed: %w", err)
	}
	defer app.close()
	if config.Demo {
		if err := seedDemo(app.store); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = app.collector.start(ctx, config.SyslogHost, config.SyslogPort); err != nil {
		return errors.New("syslog listener unavailable")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(config.HTTPHost, strconv.Itoa(config.HTTPPort)))
	if err != nil {
		cancel()
		app.collector.stop()
		return errors.New("HTTP listener unavailable")
	}
	server := &http.Server{Handler: app.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(netutil.LimitListener(listener, 64)) }()
	log.Printf("ROS Watch %s (Go) started; HTTP %d, UDP %d", Version, config.HTTPPort, config.SyslogPort)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-signals:
	case err = <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Print("HTTP server stopped")
		}
	}
	cancel()
	app.collector.stop()
	shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = server.Shutdown(shutdown)
	return nil
}

func resetCLI(config Config, args []string) error {
	flags := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	username := flags.String("username", "", "Optional new username")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("passwords must not be passed as command arguments")
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("use an interactive terminal to reset the password")
	}
	fmt.Fprint(os.Stderr, "New password (8–128 characters): ")
	password, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "Repeat new password: ")
	confirm, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	if string(password) != string(confirm) {
		return errors.New("passwords do not match")
	}
	control, err := openControl(config.DataDir)
	if err != nil {
		return err
	}
	defer control.db.Close()
	if err = control.resetPassword(context.Background(), *username, string(password)); err != nil {
		return err
	}
	fmt.Println("Account updated. Previous web sessions have been invalidated.")
	return nil
}

func seedDemo(store *Store) error {
	var count int
	if err := store.reader.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil || count > 0 {
		return err
	}
	batch := make([]Event, 0, 250)
	now := unixNow()
	mapping := store.settings.Load().WANMap
	for i := 0; i < 1200; i++ {
		port := []int{22, 80, 443, 8291, 8728, 3389, 8080, 23}[i%8]
		src := []int{8, 8, 8, 21, 21, 47, 89, 102, 155, 201}[i%10]
		iface := []string{"pppoe-out1", "pppoe-out1", "pppoe-out2"}[i%3]
		chain, prefix := "input", "RW-INPUT"
		if port == 80 || port == 443 {
			chain, prefix = "forward", "RW-FWD"
		}
		raw := fmt.Sprintf("%s %s: in:%s out:(none), proto TCP (SYN), 198.51.100.%d:%d->192.0.2.1:%d, len 60", prefix, chain, iface, src, 40000+i, port)
		event := parseMessage([]byte(raw), "127.0.0.1", mapping, now-float64(i*71))
		if event != nil {
			batch = append(batch, *event)
		}
	}
	for i := 0; i < 32; i++ {
		raw := fmt.Sprintf("RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), 203.0.113.45:%d->192.0.2.1:%d, len 60", 50000+i, 1000+i)
		if e := parseMessage([]byte(raw), "127.0.0.1", mapping, now-1800); e != nil {
			batch = append(batch, *e)
		}
	}
	// A recent, smaller scan must precede an older, busier source.
	for i := 0; i < 16; i++ {
		raw := fmt.Sprintf("RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), 203.0.113.89:%d->192.0.2.1:%d, len 60", 51000+i, 2000+i)
		if e := parseMessage([]byte(raw), "127.0.0.1", mapping, now-20); e != nil {
			batch = append(batch, *e)
		}
	}
	var mqtt Event
	for i := 0; i < 1600; i++ {
		raw := fmt.Sprintf("RW-FWD forward: in:pppoe-out2 out:bridge, proto TCP (SYN), 198.51.100.200:%d->192.0.2.50:1883, len 60", 44000+i)
		if e := parseMessage([]byte(raw), "127.0.0.1", mapping, now-float64(i*16)); e != nil {
			mqtt = *e
			batch = append(batch, *e)
		}
	}
	for i := 0; i < 60; i++ {
		raw := fmt.Sprintf("forward: in:pppoe-out1 out:bridge, connection-state:established,snat proto TCP (ACK,PSH), 203.0.113.120:443->192.0.2.20:%d, len 80", 49000+i)
		if e := parseMessage([]byte(raw), "127.0.0.1", mapping, now-float64(i*15)); e != nil {
			batch = append(batch, *e)
		}
		raw = fmt.Sprintf("forward: in:pppoe-out1 out:bridge, proto UDP, 203.0.113.121:53->192.0.2.20:%d, len 80", 54000+i)
		if e := parseMessage([]byte(raw), "127.0.0.1", mapping, now-float64(i*15)); e != nil {
			batch = append(batch, *e)
		}
	}
	for i := 0; i < 6; i++ {
		ts := float64(int64(now/300)*300) + float64(i)
		if ts > now {
			ts = now
		}
		batch = append(batch, Event{Log: &LogEvent{TS: ts, Sender: "127.0.0.1", Device: "demo-router", Category: "login", Severity: "warning", Username: "demo-user", Source: "203.0.113.76", Method: "winbox", Event: "failure", Message: "演示：登录失败", Raw: "演示：连续登录失败合成记录"}})
	}
	batch = append(batch, Event{Log: &LogEvent{TS: now - 90, Sender: "127.0.0.1", Device: "demo-router", Category: "config", Severity: "info", Event: "change", Message: "演示：调整服务端口映射", Raw: "演示：配置变更合成记录"}})
	sort.SliceStable(batch, func(i, j int) bool {
		a, b := batch[i].TS, batch[j].TS
		if batch[i].Log != nil {
			a = batch[i].Log.TS
		}
		if batch[j].Log != nil {
			b = batch[j].Log.TS
		}
		return a < b
	})
	if err := store.append(context.Background(), batch); err != nil {
		return err
	}
	return store.saveBusiness(context.Background(), BusinessRule{Name: "演示 MQTT 上报", Sender: mqtt.Sender, Src: mqtt.Src, Dst: mqtt.Dst, Proto: mqtt.Proto, Dport: 1883, Scope: mqtt.Scope, Interface: mqtt.Interface})
}
