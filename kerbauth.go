package mssql

import (
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	"github.com/jcmturner/gokrb5/v8/client"
	"github.com/jcmturner/gokrb5/v8/config"
	"github.com/jcmturner/gokrb5/v8/credentials"
	"github.com/jcmturner/gokrb5/v8/keytab"
	"github.com/jcmturner/gokrb5/v8/messages"
	"github.com/jcmturner/gokrb5/v8/spnego"
	"github.com/jcmturner/gokrb5/v8/types"
)

type krb5Auth struct {
	username          string
	realm             string
	serverSPN         string
	password          string
	port              uint64
	krb5ConfFile      string
	krbFile           string
	initkrbwithkeytab bool
	krb5Client        *client.Client
	state             krb5ClientState
}

var clientWithKeytab = client.NewWithKeytab
var loadCCache = credentials.LoadCCache
var clientFromCCache = client.NewFromCCache
var spnegoNewNegToken = spnego.NewNegTokenInitKRB5
var spnegoToken spnego.SPNEGOToken
var spnegoUnmarshal = spnegoToken.Unmarshal
var kt = &keytab.Keytab{}
var ktUnmarshal = kt.Unmarshal

var negTokenMarshal = func(negTok spnego.NegTokenInit) ([]byte, error) {
	return negTok.Marshal()
}
var getServiceTicket = func(cl *client.Client, spn string) (messages.Ticket, types.EncryptionKey, error) {
	return cl.GetServiceTicket(spn)
}

func getKRB5Auth(user, serverSPN, krb5Conf, krbFile, password string, initkrbwithkeytab bool) (auth, bool) {
	var port uint64
	var realm string
	var serviceStr string
	var err error

	params1 := strings.Split(serverSPN, ":")
	if len(params1) != 2 {
		return nil, false
	}

	params2 := strings.Split(params1[1], "@")
	switch len(params2) {
	case 1:
		port, err = strconv.ParseUint(params1[1], 10, 16)
		if err != nil {
			return nil, false
		}

	case 2:
		port, err = strconv.ParseUint(params2[0], 10, 16)
		if err != nil {
			return nil, false
		}
	default:
		return nil, false
	}

	params3 := strings.Split(serverSPN, "@")
	switch len(params3) {
	case 1:
		serviceStr = params3[0]
		params3 = strings.Split(params1[0], "/")
		params3 = strings.Split(params3[1], ".")
		realm = params3[1] + "." + params3[2]

	case 2:
		realm = params3[1]
		serviceStr = params3[0]

	default:
		return nil, false
	}

	return &krb5Auth{
		username:          user,
		serverSPN:         serviceStr,
		port:              port,
		realm:             realm,
		krb5ConfFile:      krb5Conf,
		krbFile:           krbFile,
		password:          password,
		initkrbwithkeytab: initkrbwithkeytab,
	}, true

}

func (auth *krb5Auth) InitialBytes() ([]byte, error) {
	var err error
	krb5CnfFile, err := os.Open(auth.krb5ConfFile)
	if err != nil {
		return []byte{}, err
	}
	c, err := config.NewFromReader(krb5CnfFile)
	if err != nil {
		return []byte{}, err
	}

	// Set to lookup KDCs in DNS
	c.LibDefaults.DNSLookupKDC = false

	var cl *client.Client
	// Init keytab from conf
	if auth.initkrbwithkeytab {

		keytabConf, err := ioutil.ReadFile(auth.krbFile)
		if err != nil {
			return []byte{}, err
		}
		if err = ktUnmarshal([]byte(keytabConf)); err != nil {
			return []byte{}, err
		}

		// Init krb5 client and login
		cl = clientWithKeytab(auth.username, auth.realm, kt, c, client.DisablePAFXFAST(true))

	} else {
		cache, err := loadCCache(auth.krbFile)
		if err != nil {
			return []byte{}, err
		}

		cl, err = clientFromCCache(cache, c)
		if err != nil {
			return []byte{}, err
		}
	}

	auth.krb5Client = cl
	auth.state = InitiatorStart

	tkt, sessionKey, err := getServiceTicket(cl, auth.serverSPN)
	if err != nil {
		return []byte{}, err
	}

	negTok, err := spnegoNewNegToken(auth.krb5Client, tkt, sessionKey)
	if err != nil {
		return []byte{}, err
	}

	outToken, err := negTokenMarshal(negTok)
	if err != nil {
		return []byte{}, err
	}
	auth.state = InitiatorWaitForMutal
	return outToken, nil
}

func (auth *krb5Auth) Free() {
	auth.krb5Client.Destroy()
}

func (auth *krb5Auth) NextBytes(token []byte) ([]byte, error) {
	if err := spnegoUnmarshal(token); err != nil {
		err := fmt.Errorf("unmarshal APRep token failed: %w", err)
		return []byte{}, err
	}
	auth.state = InitiatorReady
	return []byte{}, nil
}
