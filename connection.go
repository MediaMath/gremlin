package gremlin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type GoGremlin interface {
	ExecQuery(query string) ([]byte, error)
	Close() error
	Reconnect(urlStr string) error
	MaintainConnection(urlStr string) error
}

// GremlinConnections include the necessary info to connect to the server and the underlying socket
type GremlinConnection struct {
	Remote         *url.URL
	Ws             *websocket.Conn
	Auth           []OptAuth
	VerboseLogging bool
}

func NewGremlinConnection(urlStr string, options ...OptAuth) (*GremlinConnection, error) {
	r, err := url.Parse(urlStr)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{}
	ws, _, err := dialer.Dial(urlStr, http.Header{})
	if err != nil {
		return nil, err
	}
	return &GremlinConnection{Remote: r, Ws: ws, Auth: options}, nil
}

func NewVerboseGremlinConnection(urlStr string, verboseLogging bool, options ...OptAuth) (*GremlinConnection, error) {
	conn, err := NewGremlinConnection(urlStr, options...)
	if err != nil {
		return nil, err
	}
	conn.SetLogVerbosity(verboseLogging)
	return conn, nil
}

func (c *GremlinConnection) SetLogVerbosity(verboseLogging bool) {
	c.VerboseLogging = verboseLogging
}

// GremlinConnection executes the provided request
func (c *GremlinConnection) ExecQuery(query string) ([]byte, error) {
	req, err := Query(query)
	if err != nil {
		return nil, err
	}
	return c.Exec(req)
}

func (c *GremlinConnection) Exec(req *Request) ([]byte, error) {
	requestMessage, err := GraphSONSerializer(req)
	if err != nil {
		return nil, err
	}

	// Open a TCP connection
	if err = c.Ws.WriteMessage(websocket.BinaryMessage, requestMessage); err != nil {
		print("error", err)
		return nil, err
	}
	return c.ReadResponse()
}

func (c *GremlinConnection) ReadResponse() (data []byte, err error) {
	// Data buffer
	var message []byte
	var dataItems []json.RawMessage
	inBatchMode := false
	// Receive data
	for {
		if _, message, err = c.Ws.ReadMessage(); err != nil {
			return
		}
		var res *Response
		if err = json.Unmarshal(message, &res); err != nil {
			return
		}
		var items []json.RawMessage
		switch res.Status.Code {
		case StatusNoContent:
			return

		case StatusAuthenticate:
			return c.Authenticate(res.RequestId)
		case StatusPartialContent:
			inBatchMode = true
			if err = json.Unmarshal(res.Result.Data, &items); err != nil {
				return
			}
			dataItems = append(dataItems, items...)

		case StatusSuccess:
			if inBatchMode {
				if err = json.Unmarshal(res.Result.Data, &items); err != nil {
					return
				}
				dataItems = append(dataItems, items...)
				data, err = json.Marshal(dataItems)
			} else {
				data = res.Result.Data
			}
			return

		default:
			msg, exists := ErrorMsg[res.Status.Code]

			if !exists {
				err = errors.New("An unknown error occured")
			} else if !c.VerboseLogging {
				err = errors.New(msg)
			} else {
				err = errors.New(fmt.Sprintf("%d error: %s. See additional details below:\nMessage: %s", res.Status.Code, msg, res.Status.Message))
			}
			return
		}
	}
}

func (c *GremlinConnection) Reconnect(urlStr string) error {
	dialer := websocket.Dialer{}
	ws, _, err := dialer.Dial(urlStr, http.Header{})
	c.Ws = ws
	return err
}

func (c *GremlinConnection) Close() error {
	return c.Ws.Close()
}

// AuthInfo includes all info related with SASL authentication with the Gremlin server
// ChallengeId is the  requestID in the 407 status (AUTHENTICATE) response given by the server.
// We have to send an authentication request with that same RequestID in order to solve the challenge.
type AuthInfo struct {
	ChallengeId string
	User        string
	Pass        string
}

type OptAuth func(*AuthInfo) error

// Constructor for different authentication possibilities
func NewAuthInfo(options ...OptAuth) (*AuthInfo, error) {
	auth := &AuthInfo{}
	for _, op := range options {
		err := op(auth)
		if err != nil {
			return nil, err
		}
	}
	return auth, nil
}

// Sets authentication info from environment variables GREMLIN_USER and GREMLIN_PASS
func OptAuthEnv() OptAuth {
	return func(auth *AuthInfo) error {
		user, ok := os.LookupEnv("GREMLIN_USER")
		if !ok {
			return errors.New("Variable GREMLIN_USER is not set")
		}
		pass, ok := os.LookupEnv("GREMLIN_PASS")
		if !ok {
			return errors.New("Variable GREMLIN_PASS is not set")
		}
		auth.User = user
		auth.Pass = pass
		return nil
	}
}

// Sets authentication information from username and password
func OptAuthUserPass(user, pass string) OptAuth {
	return func(auth *AuthInfo) error {
		auth.User = user
		auth.Pass = pass
		return nil
	}
}

// Authenticates the connection
func (c *GremlinConnection) Authenticate(requestId string) ([]byte, error) {
	auth, err := NewAuthInfo(c.Auth...)
	if err != nil {
		return nil, err
	}
	var sasl []byte
	sasl = append(sasl, 0)
	sasl = append(sasl, []byte(auth.User)...)
	sasl = append(sasl, 0)
	sasl = append(sasl, []byte(auth.Pass)...)
	saslEnc := base64.StdEncoding.EncodeToString(sasl)
	args := &RequestArgs{Sasl: saslEnc}
	authReq := &Request{
		RequestId: requestId,
		Processor: "trasversal",
		Op:        "authentication",
		Args:      args,
	}
	return c.Exec(authReq)
}

// Send a dummy query to neptune
// If there is a network error, attempt to reconnect
func (c *GremlinConnection) MaintainConnection(urlStr string) error {
	simpleQuery := `g.V().limit(0)`

	_, err := c.ExecQuery(simpleQuery)
	if err == nil {
		return nil
	}

	_, isNetErr := err.(*net.OpError) // check if err is a network error
	if err != nil && !isNetErr {      // if it's not network error, so something else went wrong, no point in retrying
		return err
	}
	// if it is a network error, attempt to reconnect
	err = c.Reconnect(urlStr)
	if err != nil {
		return err
	}
	return nil
}

var servers []*url.URL

func NewCluster(s ...string) (err error) {
	servers = nil
	// If no arguments use environment variable
	if len(s) == 0 {
		connString := strings.TrimSpace(os.Getenv("GREMLIN_SERVERS"))
		if connString == "" {
			err = errors.New("No servers set. Configure servers to connect to using the GREMLIN_SERVERS environment variable.")
			return
		}
		servers, err = SplitServers(connString)
		return
	}
	// Else use the supplied servers
	for _, v := range s {
		var u *url.URL
		if u, err = url.Parse(v); err != nil {
			return
		}
		servers = append(servers, u)
	}
	return
}

func SplitServers(connString string) (servers []*url.URL, err error) {
	serverStrings := strings.Split(connString, ",")
	if len(serverStrings) < 1 {
		err = errors.New("Connection string is not in expected format. An example of the expected format is 'ws://server1:8182, ws://server2:8182'.")
		return
	}
	for _, serverString := range serverStrings {
		var u *url.URL
		if u, err = url.Parse(strings.TrimSpace(serverString)); err != nil {
			return
		}
		servers = append(servers, u)
	}
	return
}

func CreateConnection() (conn net.Conn, server *url.URL, err error) {
	connEstablished := false
	for _, s := range servers {
		c, err := net.DialTimeout("tcp", s.Host, 1*time.Second)
		if err != nil {
			continue
		}
		connEstablished = true
		conn = c
		server = s
		break
	}
	if !connEstablished {
		err = errors.New("Could not establish connection. Please check your connection string and ensure at least one server is up.")
	}
	return
}
