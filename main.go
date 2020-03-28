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

	irc "github.com/fluffle/goirc/client"
	"golang.org/x/crypto/bcrypt"
)

const botMessage = `confann by alrs@tilde.team answers to "!botlist", and "!botlist" alone.`
const confDir = ".confann"
const channel = "#alrs"
const ircServer = "tilde.chat"
const ircPort = "6697"

var ircReady bool

type passwd struct {
	User string
	Hash string
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

	passwd, err := loadPasswd()
	if err != nil {
		log.Fatalf("loadPasswd: %v", err)
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

			// from Asterisk dialplan:
			// exten => 1000,1,Set(CURLOPT(userpwd)=some_username:some_password)
			// exten => 1000,n,NoOp(${CURL(https://confann.example.org/,CLID=${CALLERID(num)}})
			// exten => 1000,n,ConfBridge("someconference")

			u, p, authPresent := r.BasicAuth()
			if !authPresent || u != passwd.User {
				log.Printf("401: %s", r.URL.RequestURI())
				http.Error(w, "401", http.StatusUnauthorized)
				return
			}
			cryptRes := bcrypt.CompareHashAndPassword([]byte(passwd.Hash), []byte(p))
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
			ircReady = true
			log.Print("IRC CONNECTED")
			// tilde.chat requires join to bots
			conn.Join("#bots")
			conn.Join(channel)
			log.Printf("JOIN %s", channel)
		case <-handlerChans["disconnected"]:
			ircReady = false
			log.Print("IRC DISCONNECTED")
			quit <- struct{}{}
		}
	}
}
