package main

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"

	//	"github.com/davecgh/go-spew/spew"
	irc "github.com/fluffle/goirc/client"
)

const botMessage = `confann by alrs@tilde.team answers to "!botlist", and "!botlist" alone.`
const confDir = ".confann"
const channel = "#alrs"
const ircServer = "tilde.chat"
const ircPort = "6697"

func loadNickservPW() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	fqp := path.Join(home, confDir, "nickserv.secret")
	data, err := ioutil.ReadFile(fqp)
	if err != nil {
		return "", err
	}
	pass := strings.TrimSuffix(string(data), "\n")
	if pass == "" {
		return "", fmt.Errorf("empty nickserv secret")
	}
	return pass, nil
}

func identString(pw string) string {
	return fmt.Sprintf("identify %s", pw)
}

func serverString() string {
	return ircServer + ":" + ircPort
}

func buildIRCConfig() (*irc.Config, error) {
	cfg := irc.NewConfig("confann", "alrs")
	cfg.Server = serverString()
	cfg.SSL = true
	cfg.SSLConfig = &tls.Config{ServerName: ircServer}
	cfg.Flood = false
	return cfg, nil
}

func defineHandlers(conn *irc.Conn, pw string) map[string]chan struct{} {
	handlerChans := make(map[string]chan struct{})
	handlerChans["connected"] = make(chan struct{})
	handlerChans["disconnect"] = make(chan struct{})

	conn.HandleFunc("connected",
		func(con *irc.Conn, line *irc.Line) {
			handlerChans["connected"] <- struct{}{}
		})

	conn.HandleFunc("disconnected",
		func(conn *irc.Conn, line *irc.Line) {
			close(handlerChans["disconnected"])
		})

	conn.HandleFunc(irc.PRIVMSG, func(conn *irc.Conn, line *irc.Line) {
		if len(line.Args) >= 2 && line.Args[1] == "!botlist" {
			conn.Privmsg(line.Args[0], botMessage)
			log.Print(line.Raw)
		}
	})

	conn.HandleFunc(irc.REGISTER, func(conn *irc.Conn, line *irc.Line) {
		log.Print("received REGISTER")
		conn.Privmsg("NickServ", identString(pw))
	})

	return handlerChans
}

func main() {
	quit := make(chan struct{}, 1)
	sigs := make(chan os.Signal)
	signal.Notify(sigs, os.Interrupt)

	nickservPW, err := loadNickservPW()
	if err != nil {
		log.Fatalf("loadNickservPW: %v", err)
	}

	cfg, err := buildIRCConfig()
	if err != nil {
		log.Fatalf("buildIRCConfig: %v", err)
	}

	conn := irc.Client(cfg)
	//	conn.EnableStateTracking()
	handlerChans := defineHandlers(conn, nickservPW)

	if err := conn.ConnectTo(cfg.Server); err != nil {
		log.Fatalf("ConnectTo: %v", err)
	}
	log.Printf("ConnectTo: %s", serverString())

	go func() {
		handler := func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				log.Printf("404: %s", r.URL.RequestURI())
				http.Error(w, "404", http.StatusNotFound)
				return
			}
			conn.Privmsg(channel, r.URL.Path)
		}
		http.HandleFunc("/", handler)
		http.ListenAndServe(":8080", nil)
	}()

	for {
		select {
		case <-quit:
			conn.Close()
			os.Exit(0)
		case <-sigs:
			log.Print("INTERRUPT")
			quit <- struct{}{}
		case <-handlerChans["connected"]:
			log.Print("IRC CONNECTED")
			conn.Join(channel)
			log.Printf("JOIN %s", channel)
		case <-handlerChans["disconnected"]:
			log.Print("IRC DISCONNECTED")
			quit <- struct{}{}
		}
	}
}
