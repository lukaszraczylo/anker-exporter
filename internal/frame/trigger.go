// Package frame builds and (later) decodes Anker SOLIX hex frames.
package frame

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	devicePN         = "A1761"
	triggerMsgType   = "\x00\x57"
	framePrefix      = "\xff\x09"
	sendPattern      = "\x03\x00\x0f"
	typeUnsignedByte = 0x01
	typeVar          = 0x03
	fieldPattern     = 0xa1
	fieldEnable      = 0xa2
	fieldTimeout     = 0xa3
	fieldTimestamp   = 0xfe
	cmdPublish       = 17

	// MinTriggerTimeout and MaxTriggerTimeout bound the realtime trigger timeout in seconds.
	MinTriggerTimeout = 30
	MaxTriggerTimeout = 600
)

type head struct {
	Version   string `json:"version"`
	ClientID  string `json:"client_id"`
	SessID    string `json:"sess_id"`
	DevicePN  string `json:"device_pn"`
	DeviceSN  string `json:"device_sn"`
	Timestamp int64  `json:"timestamp"`
	MsgSeq    int    `json:"msg_seq"`
	Seed      int    `json:"seed"`
	CmdStatus int    `json:"cmd_status"`
	Cmd       int    `json:"cmd"`
	SignCode  int    `json:"sign_code"`
}

type payload struct {
	DeviceSN  string `json:"device_sn"`
	AccountID string `json:"account_id"`
	Data      string `json:"data"`
}

// HeadClientID is the client_id the app puts in the command envelope head.
func HeadClientID(appName, userID, certificateID string) string {
	return fmt.Sprintf("android-%s-%s-%s", appName, userID, certificateID)
}

// BuildTrigger returns the MQTT envelope for the realtime trigger (message 0057,
// a2=1, a3=timeoutSec). Publish it to cmd/anker_power/A1761/{sn}/req.
func BuildTrigger(sn, userID, clientID string, timeoutSec int, now time.Time) ([]byte, error) {
	if sn == "" || userID == "" {
		return nil, errors.New("trigger: serial and user id are required")
	}
	if timeoutSec < MinTriggerTimeout || timeoutSec > MaxTriggerTimeout {
		return nil, fmt.Errorf("trigger: timeout %ds out of range %d..%d", timeoutSec, MinTriggerTimeout, MaxTriggerTimeout)
	}
	frame := triggerFrame(uint32(timeoutSec), uint32(now.Unix())) //nolint:gosec // bounded above; unix seconds fit 32 bits until 2106
	pl, err := json.Marshal(payload{DeviceSN: sn, AccountID: userID, Data: base64.StdEncoding.EncodeToString(frame)})
	if err != nil {
		return nil, fmt.Errorf("trigger: encode payload: %w", err)
	}
	return json.Marshal(struct {
		Payload string `json:"payload"`
		Head    head   `json:"head"`
	}{
		Head: head{
			Version: "1.0.0.1", ClientID: clientID, SessID: "1234-5678", MsgSeq: 1, Seed: 1,
			Timestamp: now.Unix(), CmdStatus: 2, Cmd: cmdPublish, SignCode: 1,
			DevicePN: devicePN, DeviceSN: sn,
		},
		Payload: string(pl),
	})
}

// triggerFrame fields are sorted by name (a1, a2, a3, fe), as the reference library sends them.
func triggerFrame(timeout, ts uint32) []byte {
	b := []byte(framePrefix + "\x00\x00" + sendPattern + triggerMsgType)
	b = append(b, fieldPattern, 0x01, 0x22)
	b = append(b, fieldEnable, 0x02, typeUnsignedByte, 0x01)
	b = append(b, fieldTimeout, 0x05, typeVar)
	b = binary.LittleEndian.AppendUint32(b, timeout)
	b = append(b, fieldTimestamp, 0x05, typeVar)
	b = binary.LittleEndian.AppendUint32(b, ts)
	binary.LittleEndian.PutUint16(b[2:4], uint16(len(b)+1)) //nolint:gosec // frame is 30 bytes
	var x byte
	for _, c := range b {
		x ^= c
	}
	return append(b, x)
}
