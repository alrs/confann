generate bcrypt `~/.confann/passwd` file for your given `USERNAME`:

`htpasswd -c -B -C 12 passwd USERNAME`

in Asterisk dialplan:
```
exten => 1000,1,Set(CURLOPT(userpwd)=some_username:some_password)
exten => 1000,n,NoOp(${CURL(https://confann.example.org/,CLID=${CALLERID(num)}})
exten => 1000,n,ConfBridge("someconference")


