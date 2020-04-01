//	confann, an IRC bot to announce Asterisk conference joins
// 	Copyright (C) 2020 Lars Lehtonen

//	This program is free software: you can redistribute it and/or modify
//	it under the terms of the GNU General Public License as published by
//	the Free Software Foundation, either version 3 of the License, or
//	(at your option) any later version.

//	This program is distributed in the hope that it will be useful,
//	but WITHOUT ANY WARRANTY; without even the implied warranty of
//	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//	GNU General Public License for more details.

//	You should have received a copy of the GNU General Public License
//	along with this program.  If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"time"

	irc "github.com/fluffle/goirc/client"
	"golang.org/x/crypto/bcrypt"
)

const botMessage = `confann by alrs@tilde.team answers to "!botlist", and "!botlist" alone.`
const confDir = ".confann"

var ircReady bool

type passwd struct {
	User string
	Hash string
}

var channel, ircServer, ircPort, port string

func init() {
	flag.StringVar(&channel, "channel", "#alrs", "IRC channel")
	flag.StringVar(&ircServer, "server", "tilde.chat", "IRC server")
	flag.StringVar(&ircPort, "ircPort", "6697", "IRC port")
	flag.StringVar(&port, "apiPort", "8080", "API port")
	flag.Parse()
}

func parsePasswd(data []byte) (passwd, error) {
	var p passwd
	pairString := strings.TrimSuffix(string(data), "\n")
	pair := strings.Split(pairString, ":")
	if len(pair) != 2 {
		return p, fmt.Errorf("passwd record has more than one seperator")
	}
	p.User = pair[0]
	p.Hash = pair[1]
	return p, nil
}

func loadPasswd() (passwd, error) {
	var p passwd
	home, err := os.UserHomeDir()
	if err != nil {
		return p, err
	}
	fqp := path.Join(home, confDir, "passwd")
	data, err := ioutil.ReadFile(fqp)
	if err != nil {
		return p, err
	}
	return parsePasswd(data)
}

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
	cfg := irc.NewConfig("confann", "confann")
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

func wrapAPIHandler(conn *irc.Conn, pw passwd) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {

		// from Asterisk dialplan:
		// exten => 1000,1,Set(CURLOPT(userpwd)=some_username:some_password)
		// exten => 1000,n,NoOp(${CURL(https://confann.example.org/,CLID=${CALLERID(num)}})
		// exten => 1000,n,ConfBridge("someconference")

		u, p, authPresent := r.BasicAuth()
		if !authPresent || u != pw.User {
			log.Printf("401: %s", r.URL.RequestURI())
			http.Error(w, "401", http.StatusUnauthorized)
			return
		}
		cryptRes := bcrypt.CompareHashAndPassword([]byte(pw.Hash), []byte(p))
		if cryptRes != nil {
			log.Printf("401: %s %v", r.URL.RequestURI(), cryptRes)
			http.Error(w, "401", http.StatusUnauthorized)
			return
		}

		if r.Method != "POST" {
			log.Printf("404: %s", r.URL.RequestURI())
			http.Error(w, "404", http.StatusNotFound)
			return
		}
		err := r.ParseForm()
		if err != nil {
			log.Printf("error parsing request form: %v", err)
			http.Error(w, "400", http.StatusBadRequest)
			return
		}
		var post []string
		var clid string
		var ok bool
		if post, ok = r.PostForm["CLID"]; ok {
			log.Printf("API: %v from %s", post, r.RemoteAddr)
			if len(post) > 0 {
				clid = post[0]
			} else {
				clid = "<< anonymous caller >>"
			}
		} else {
			log.Printf("API: insufficient PostForm: %v", r.PostForm)
			http.Error(w, "400", http.StatusBadRequest)
			return
		}
		notice := fmt.Sprintf("%s joined the conference.", clid)

		if !ircReady {
			log.Print("API: irc not connected yet")
			http.Error(w, "503: irc disconnected", http.StatusServiceUnavailable)
			return
		}
		conn.Notice(channel, notice)
	}
}

func main() {
	quit := make(chan struct{}, 1)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)

	nickservPW, err := loadNickservPW()
	if err != nil {
		log.Fatalf("loadNickservPW: %v", err)
	}

	cfg, err := buildIRCConfig()
	if err != nil {
		log.Fatalf("buildIRCConfig: %v", err)
	}

	pw, err := loadPasswd()
	if err != nil {
		log.Fatalf("loadPasswd: %v", err)
	}

	conn := irc.Client(cfg)
	//	conn.EnableStateTracking()
	handlerChans := defineHandlers(conn, nickservPW)

	if err := conn.ConnectTo(cfg.Server); err != nil {
		log.Fatalf("ConnectTo: %v", err)
	}
	log.Printf("DIAL: %s", serverString())

	handler := wrapAPIHandler(conn, pw)
	srv := &http.Server{
		Addr: ":" + port,
	}
	errCh := make(chan error, 1)
	go func() {
		http.HandleFunc("/", handler)
		errCh <- srv.ListenAndServe()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for {
		select {
		case <-quit:
			log.Print("closing IRC connection")
			conn.Close()
			log.Print("shutting down HTTP server")
			srv.Shutdown(ctx)
			os.Exit(0)
		case <-sigs:
			log.Print("** interrupt **")
			quit <- struct{}{}
		case <-handlerChans["connected"]:
			ircReady = true
			log.Print("irc connected")
			// tilde.chat requires join to bots
			conn.Join("#bots")
			log.Printf("joining %s", channel)
			conn.Join(channel)
		case <-handlerChans["disconnected"]:
			ircReady = false
			log.Print("irc disconnected")
			quit <- struct{}{}
		case err := <-errCh:
			log.Printf("api server error: %v", err)
			quit <- struct{}{}
		}
	}
}
