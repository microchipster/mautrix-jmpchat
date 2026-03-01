package xmpp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	"gosrc.io/xmpp/stanza"
)

type Credential struct {
	secret     string
	mechanisms []string
}

func Password(pwd string) Credential {
	credential := Credential{
		secret:     pwd,
		mechanisms: []string{"SCRAM-SHA-512", "SCRAM-SHA-256", "SCRAM-SHA-1", "PLAIN"},
	}
	return credential
}

func OAuthToken(token string) Credential {
	credential := Credential{
		secret:     token,
		mechanisms: []string{"X-OAUTH2"},
	}
	return credential
}

func authSASL(socket io.ReadWriter, decoder *xml.Decoder, f stanza.StreamFeatures, user string, credential Credential) (err error) {
	var matchingMech string
	for _, mech := range credential.mechanisms {
		if isSupportedMech(mech, f.Mechanisms.Mechanism) {
			matchingMech = mech
			break
		}
	}

	switch matchingMech {
	case "SCRAM-SHA-512":
		return authScram(socket, decoder, matchingMech, user, credential.secret)
	case "SCRAM-SHA-256":
		return authScram(socket, decoder, matchingMech, user, credential.secret)
	case "SCRAM-SHA-1":
		return authScram(socket, decoder, matchingMech, user, credential.secret)
	case "PLAIN", "X-OAUTH2":
		return authPlain(socket, decoder, matchingMech, user, credential.secret)
	default:
		err := fmt.Errorf("no matching authentication (%v) supported by server: %v", credential.mechanisms, f.Mechanisms.Mechanism)
		return NewConnError(err, true)
	}
}

type saslChallenge struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:xmpp-sasl challenge"`
	Value   string   `xml:",innerxml"`
}

type saslResponse struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:xmpp-sasl response"`
	Value   string   `xml:",innerxml"`
}

func authPlain(socket io.ReadWriter, decoder *xml.Decoder, mech string, user string, secret string) error {
	raw := "\x00" + user + "\x00" + secret
	enc := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
	base64.StdEncoding.Encode(enc, []byte(raw))

	a := stanza.SASLAuth{
		Mechanism: mech,
		Value:     string(enc),
	}
	data, err := xml.Marshal(a)
	if err != nil {
		return err
	}
	n, err := socket.Write(data)
	if err != nil {
		return err
	} else if n == 0 {
		return errors.New("failed to write authSASL nonza to socket : wrote 0 bytes")
	}

	val, err := stanza.NextPacket(decoder)
	if err != nil {
		return err
	}

	switch v := val.(type) {
	case stanza.SASLSuccess:
	case stanza.SASLFailure:
		err := errors.New("auth failure: " + v.Any.Local)
		return NewConnError(err, true)
	default:
		return errors.New("expected SASL success or failure, got " + v.Name())
	}
	return err
}

func authScram(socket io.ReadWriter, decoder *xml.Decoder, mech string, user string, secret string) error {
	clientNonce, err := scramNonce()
	if err != nil {
		return err
	}

	clientFirstBare := fmt.Sprintf("n=%s,r=%s", scramEscape(user), clientNonce)
	gs2Header := "n,,"
	clientFirstMessage := gs2Header + clientFirstBare

	if err := sendSASLAuth(socket, mech, clientFirstMessage); err != nil {
		return err
	}

	challenge, err := readSASLChallenge(decoder)
	if err != nil {
		return err
	}
	serverFirstBytes, err := base64.StdEncoding.DecodeString(challenge)
	if err != nil {
		return err
	}
	serverFirst := string(serverFirstBytes)
	serverFields := parseSCRAMFields(serverFirst)
	serverNonce := serverFields["r"]
	if serverNonce == "" || !strings.HasPrefix(serverNonce, clientNonce) {
		return NewConnError(errors.New("scram: server nonce missing or invalid"), true)
	}

	saltB64 := serverFields["s"]
	if saltB64 == "" {
		return NewConnError(errors.New("scram: server salt missing"), true)
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return err
	}

	iterStr := serverFields["i"]
	iter, err := strconv.Atoi(iterStr)
	if err != nil || iter <= 0 {
		return NewConnError(errors.New("scram: invalid iteration count"), true)
	}

	clientFinalWithoutProof := fmt.Sprintf("c=biws,r=%s", serverNonce)
	authMessage := clientFirstBare + "," + serverFirst + "," + clientFinalWithoutProof

	var h func() hash.Hash
	switch mech {
	case "SCRAM-SHA-512":
		h = sha512.New
	case "SCRAM-SHA-256":
		h = sha256.New
	default:
		h = sha1.New
	}

	saltedPassword := pbkdf2.Key([]byte(secret), salt, iter, h().Size(), h)
	clientKey := hmacHash(h, saltedPassword, []byte("Client Key"))

	hashInst := h()
	hashInst.Write(clientKey)
	storedKey := hashInst.Sum(nil)

	clientSignature := hmacHash(h, storedKey, []byte(authMessage))
	clientProof := xorBytes(clientKey, clientSignature)
	clientProofB64 := base64.StdEncoding.EncodeToString(clientProof)

	clientFinal := clientFinalWithoutProof + ",p=" + clientProofB64
	if err := sendSASLResponse(socket, clientFinal); err != nil {
		return err
	}

	val, err := stanza.NextPacket(decoder)
	if err != nil {
		return err
	}

	switch v := val.(type) {
	case stanza.SASLSuccess:
		if v.Value == "" {
			return nil
		}
		serverFinalBytes, err := base64.StdEncoding.DecodeString(v.Value)
		if err != nil {
			return err
		}
		serverFinal := string(serverFinalBytes)
		finalFields := parseSCRAMFields(serverFinal)
		if errMsg := finalFields["e"]; errMsg != "" {
			return NewConnError(errors.New("scram auth failure: "+errMsg), true)
		}
		serverVerifier := finalFields["v"]
		if serverVerifier == "" {
			return NewConnError(errors.New("scram: missing server verifier"), true)
		}
		serverKey := hmacHash(h, saltedPassword, []byte("Server Key"))
		serverSignature := hmacHash(h, serverKey, []byte(authMessage))
		expectedVerifier := base64.StdEncoding.EncodeToString(serverSignature)
		if serverVerifier != expectedVerifier {
			return NewConnError(errors.New("scram: server verifier mismatch"), true)
		}
		return nil
	case stanza.SASLFailure:
		err := errors.New("auth failure: " + v.Any.Local)
		return NewConnError(err, true)
	default:
		return errors.New("expected SASL success or failure, got " + val.Name())
	}
}

func sendSASLAuth(socket io.ReadWriter, mech string, payload string) error {
	enc := base64.StdEncoding.EncodeToString([]byte(payload))
	a := stanza.SASLAuth{
		Mechanism: mech,
		Value:     enc,
	}
	data, err := xml.Marshal(a)
	if err != nil {
		return err
	}
	n, err := socket.Write(data)
	if err != nil {
		return err
	} else if n == 0 {
		return errors.New("failed to write SASL auth nonza to socket : wrote 0 bytes")
	}
	return nil
}

func sendSASLResponse(socket io.ReadWriter, payload string) error {
	enc := base64.StdEncoding.EncodeToString([]byte(payload))
	r := saslResponse{Value: enc}
	data, err := xml.Marshal(r)
	if err != nil {
		return err
	}
	n, err := socket.Write(data)
	if err != nil {
		return err
	} else if n == 0 {
		return errors.New("failed to write SASL response nonza to socket : wrote 0 bytes")
	}
	return nil
}

func readSASLChallenge(decoder *xml.Decoder) (string, error) {
	se, err := stanza.NextStart(decoder)
	if err != nil {
		return "", err
	}
	if se.Name.Space != stanza.NSSASL {
		return "", errors.New("expected SASL challenge")
	}

	switch se.Name.Local {
	case "challenge":
		var ch saslChallenge
		if err := decoder.DecodeElement(&ch, &se); err != nil {
			return "", err
		}
		return ch.Value, nil
	case "failure":
		var failure stanza.SASLFailure
		if err := decoder.DecodeElement(&failure, &se); err != nil {
			return "", err
		}
		return "", NewConnError(errors.New("auth failure: "+failure.Any.Local), true)
	case "success":
		var success stanza.SASLSuccess
		if err := decoder.DecodeElement(&success, &se); err != nil {
			return "", err
		}
		return "", errors.New("unexpected SASL success without challenge")
	default:
		return "", errors.New("unexpected SASL element: " + se.Name.Local)
	}
}

func scramNonce() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

func scramEscape(value string) string {
	replacer := strings.NewReplacer("=", "=3D", ",", "=2C")
	return replacer.Replace(value)
}

func parseSCRAMFields(message string) map[string]string {
	fields := map[string]string{}
	for _, part := range strings.Split(message, ",") {
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		fields[kv[0]] = kv[1]
	}
	return fields
}

func hmacHash(h func() hash.Hash, key []byte, data []byte) []byte {
	mac := hmac.New(h, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func xorBytes(left []byte, right []byte) []byte {
	if len(left) != len(right) {
		return nil
	}
	out := make([]byte, len(left))
	for i := range left {
		out[i] = left[i] ^ right[i]
	}
	return out
}

func isSupportedMech(mech string, mechanisms []string) bool {
	for _, m := range mechanisms {
		if mech == m {
			return true
		}
	}
	return false
}
