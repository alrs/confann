package main

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"strings"

	"github.com/davecgh/go-spew/spew"
	irc "github.com/fluffle/goirc/client"
)

const botMessage = `confann by alrs@tilde.team answers to "!botlist", and "!botlist" alone.`
const confDir = ".confann"
const channel = "#alrs"

var sleepSeconds = 60

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

func buildIRCConfig() (*irc.Config, error) {
	cfg := irc.NewConfig("confann", "alrs")
	cfg.Server = "tilde.chat:6697"
	cfg.SSL = true
	cfg.SSLConfig = &tls.Config{ServerName: "tilde.chat"}
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
		}
		log.Print(spew.Sdump(line.Args))
	})

	conn.HandleFunc(irc.REGISTER, func(conn *irc.Conn, line *irc.Line) {
		log.Print("received REGISTER")
		conn.Privmsg("NickServ", identString(pw))
	})

	return handlerChans
}

func main() {
	nickservPW, err := loadNickservPW()
	if err != nil {
		log.Fatalf("loadNickservPW: %v", err)
	}

	cfg, err := buildIRCConfig()
	if err != nil {
		log.Fatalf("buildIRCConfig: %v", err)
	}

	conn := irc.Client(cfg)
	conn.EnableStateTracking()
	handlerChans := defineHandlers(conn, nickservPW)

	if err := conn.ConnectTo(cfg.Server); err != nil {
		log.Fatalf("ConnectTo: %v", err)
	}

	for {
		select {
		case <-handlerChans["connected"]:
			conn.Join(channel)
			log.Print("CONNECTED")
		case <-handlerChans["disconnected"]:
			log.Print("DISCONNECTED")
			os.Exit(0)
		}
	}
}
