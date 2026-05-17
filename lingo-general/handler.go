package general

import (
	"bytes"
	"fmt"

	"github.com/oandrew/ipod"
	"github.com/sirupsen/logrus"
)

var log = logrus.StandardLogger()

type DeviceGeneral interface {
	UIMode() UIMode
	SetUIMode(UIMode)
	Name() string
	SoftwareVersion() (major, minor, rev uint8)
	SerialNum() string

	LingoProtocolVersion(lingo uint8) (major, minor uint8)
	LingoOptions(ling uint8) uint64

	PrefSettingID(classID uint8) uint8
	SetPrefSettingID(classID, settingID uint8, restoreOnExit bool)

	StartIDPS()
	EndIDPS(status AccEndIDPSStatus)
	SetToken(token FIDTokenValue) error
	OnAuthenticationComplete()
	StoreAuthChallenge(challenge [20]byte)
	GetDeviceAuthenticationInfo() (major uint8, minor uint8, certData []byte)

	SetEventNotificationMask(mask uint64)
	EventNotificationMask() uint64
	SupportedEventNotificationMask() uint64

	CancelCommand(lingo uint8, cmd uint16, transaction uint16)

	MaxPayload() uint16
}

func ackSuccess(req *ipod.Command) *ACK {
	return &ACK{Status: ACKStatusSuccess, CmdID: uint8(req.ID.CmdID())}
}

func ackPending(req *ipod.Command, maxWait uint32) *ACKPending {
	return &ACKPending{Status: ACKStatusPending, CmdID: uint8(req.ID.CmdID()), MaxWait: maxWait}
}

func ack(req *ipod.Command, status ACKStatus) *ACK {
	return &ACK{Status: status, CmdID: uint8(req.ID.CmdID())}
}

func ackFIDTokenValue(tokenValue FIDTokenValue) FIDTokenValueACK {
	ackToken := func(token interface{}) interface{} {
		switch t := token.(type) {
		case *FIDIdentifyToken:
			return []byte{0x00}
		case *FIDAccCapsToken:
			return []byte{0x00}
		case *FIDAccInfoToken:
			return []byte{0x00, t.AccInfoType}
		case *FIDiPodPreferenceToken:
			return []byte{0x00, t.PrefClass}
		case *FIDEAProtocolToken:
			return []byte{0x00, t.ProtocolIndex}
		case *FIDBundleSeedIDPrefToken:
			return []byte{0x00}
		case *FIDScreenInfoToken:
			return []byte{0x00}
		case *FIDEAProtocolMetadataToken:
			return []byte{0x00}
		case *FIDMicrophoneCapsToken:
			return []byte{0x00}
		default:
			return nil
		}

	}

	return FIDTokenValueACK{
		ID:  tokenValue.ID,
		ACK: ackToken(tokenValue.Token),
	}
}

func ackFIDTokenValues(tokens *SetFIDTokenValues) *RetFIDTokenValueACKs {
	acks := make([]FIDTokenValueACK, len(tokens.FIDTokenValues))
	for i := range tokens.FIDTokenValues {
		acks[i] = ackFIDTokenValue(tokens.FIDTokenValues[i])
	}
	return &RetFIDTokenValueACKs{
		FIDTokenValueACKs: acks,
	}
}

var devCertBuf bytes.Buffer

func HandleGeneral(req *ipod.Command, tr ipod.CommandWriter, dev DeviceGeneral) error {
	switch msg := req.Payload.(type) {
	case *RequestRemoteUIMode:
		ipod.Respond(req, tr, &ReturnRemoteUIMode{
			Mode: ipod.BoolToByte(dev.UIMode() == UIModeExtended),
		})
	case *EnterRemoteUIMode:
		if dev.UIMode() == UIModeExtended {
			ipod.Respond(req, tr, ackSuccess(req))
		} else {
			ipod.Respond(req, tr, ackPending(req, 300))
			dev.SetUIMode(UIModeExtended)
			ipod.Respond(req, tr, ackSuccess(req))
		}
	case *ExitRemoteUIMode:
		if dev.UIMode() != UIModeExtended {
			ipod.Respond(req, tr, ackSuccess(req))
		} else {
			ipod.Respond(req, tr, ackPending(req, 300))
			dev.SetUIMode(UIModeStandart)
			ipod.Respond(req, tr, ackSuccess(req))
		}
	case *RequestiPodName:
		ipod.Respond(req, tr, &ReturniPodName{Name: ipod.StringToBytes(dev.Name())})
	case *RequestiPodSoftwareVersion:
		var resp ReturniPodSoftwareVersion
		resp.Major, resp.Minor, resp.Rev = dev.SoftwareVersion()
		ipod.Respond(req, tr, &resp)
	case *RequestiPodSerialNum:
		ipod.Respond(req, tr, &ReturniPodSerialNum{Serial: ipod.StringToBytes(dev.SerialNum())})
	case *RequestiPodModelNum:
		ipod.Respond(req, tr, &ReturniPodModelNum{
			// iphone 4
			ModelID: 0x00111349,
			Model:   ipod.StringToBytes("MC676"),
		})
	case *RequestLingoProtocolVersion:
		var resp ReturnLingoProtocolVersion
		resp.Lingo = msg.Lingo
		resp.Major, resp.Minor = dev.LingoProtocolVersion(msg.Lingo)
		ipod.Respond(req, tr, &resp)
	case *RequestTransportMaxPayloadSize:
		ipod.Respond(req, tr, &ReturnTransportMaxPayloadSize{MaxPayload: dev.MaxPayload()})
	case *Identify:
		// old iAP1 Cmd 0x01 — accessory identified itself, ACK and continue
		log.WithFields(logrus.Fields{
			"device_type": fmt.Sprintf("%02x", msg.DeviceType),
			"lingos":      fmt.Sprintf("%x", msg.Lingos),
		}).Info("[iAP1] Identify received")
		ipod.Respond(req, tr, ackSuccess(req))

	case *IdentifyDeviceLingoes:
		// Check authentication options
		switch msg.Options {
		case IdentifyDeviceLingoesOptionsDeferAuth:
			// DeferAuth is not supported
			ipod.Respond(req, tr, ack(req, ACKStatusFailed))
		case IdentifyDeviceLingoesOptionsImmediateAuth:
			// Acknowledge and continue - authentication will start after IDPS
			ipod.Respond(req, tr, ackSuccess(req))
		default:
			// NoAuth or unrecognized - proceed with normal flow
			ipod.Respond(req, tr, ackSuccess(req))
		}

	// We receive the car's authentication certificate in response to our GetDevAuthenticationInfo request
	case *RetDevAuthenticationInfo:
		log.WithFields(logrus.Fields{
			"section":    fmt.Sprintf("%d/%d", msg.CertCurrentSection, msg.CertMaxSection),
			"data_size":  len(msg.CertData),
			"auth_major": msg.Major,
			"auth_minor": msg.Minor,
		}).Info("[AUTH] RetDevAuthenticationInfo received")

		if msg.Major >= 2 {
			// Accumulate multi-section certificate
			if msg.CertCurrentSection == 0 {
				log.Info("[AUTH] Starting multi-section certificate accumulation")
				devCertBuf.Reset()
			}
			devCertBuf.Write(msg.CertData)
			log.WithField("total_size", devCertBuf.Len()).Debug("[AUTH] Certificate data accumulated")

			if msg.CertCurrentSection < msg.CertMaxSection {
				// More sections coming, just acknowledge
				log.WithField("section", msg.CertCurrentSection).Info("[AUTH] Acknowledging intermediate certificate section")
				ipod.Respond(req, tr, ackSuccess(req))
			} else {
				// All certificate sections received
				log.WithField("total_cert_size", devCertBuf.Len()).Info("[AUTH] All certificate sections received, acknowledging final section")
				// TODO: Validate certificate

				// Acknowledge receipt of complete certificate
				ipod.Respond(req, tr, &AckDevAuthenticationInfo{Status: DevAuthInfoStatusSupported})

				// Now WE send a challenge to the car to sign
				// Generate a 20-byte random challenge
				var challenge [20]byte

				// Try to use device's challenge generation if available
				if genDev, ok := dev.(interface {
					GenerateAuthChallenge() [20]byte
				}); ok {
					challenge = genDev.GenerateAuthChallenge()
				} else {
					// Fallback to zero challenge if method not available
					log.Warn("[AUTH] Device doesn't support GenerateAuthChallenge, using zero challenge")
				}

				log.WithField("challenge", fmt.Sprintf("%02x", challenge[:])).Info("[AUTH] Generated challenge, sending GetDevAuthenticationSignature to car")

				ipod.Send(tr, &GetDevAuthenticationSignatureV2{
					Challenge: challenge,
					Counter:   0x01,
				})
				log.Info("[AUTH] Waiting for car to sign challenge (RetDevAuthenticationSignature)...")
				// Car will respond with RetDevAuthenticationSignature containing its signature
			}
		} else {
			log.WithFields(logrus.Fields{
				"major": msg.Major,
				"minor": msg.Minor,
			}).Warn("[AUTH] Unsupported authentication version")
			ipod.Respond(req, tr, &AckDevAuthenticationInfo{Status: DevAuthInfoStatusSupported})
		}

	// Car requests our (accessory's) authentication info
	case *GetDevAuthenticationInfo:
		log.Info("[AUTH] GetDevAuthenticationInfo received from car - car requesting device cert")
		major, minor, certData := dev.GetDeviceAuthenticationInfo()
		log.WithFields(logrus.Fields{
			"major":     major,
			"minor":     minor,
			"cert_size": len(certData),
		}).Info("[AUTH] Sending RetDevAuthenticationInfo to car")
		// Send our certificate in response to car's request
		ipod.Respond(req, tr, &RetDevAuthenticationInfo{
			Major:              major,
			Minor:              minor,
			CertCurrentSection: 0,
			CertMaxSection:     0,
			CertData:           certData,
		})

	// We receive car's response to our signature challenge (if we sent one)
	case *RetDevAuthenticationSignature:
		log.WithField("signature_size", len(msg.Signature)).Info("[AUTH] RetDevAuthenticationSignature received from car")
		// Car has sent us its signature
		// TODO: Validate signature against their certificate
		log.Info("[AUTH] Acknowledging car's authentication signature")
		ipod.Respond(req, tr, &AckDevAuthenticationStatus{Status: DevAuthStatusPassed})
		// Authentication complete - sending AckDevAuthenticationStatus completes the handshake
		log.Info("[AUTH] Authentication complete - calling OnAuthenticationComplete()")
		dev.OnAuthenticationComplete()

	// Car sends us a challenge to sign (UNEXPECTED - we send the challenge, not receive it)
	case *GetDevAuthenticationSignatureV2:
		log.WithField("challenge", fmt.Sprintf("%02x", msg.Challenge)).Warn("[AUTH] GetDevAuthenticationSignatureV2 received from car (unexpected)")
		// In normal flow, WE send this to the car, we don't receive it
		// But if car sends this, just respond with empty signature
		log.Warn("[AUTH] Sending empty signature in response")
		ipod.Respond(req, tr, &RetDevAuthenticationSignature{
			Signature: make([]byte, 0), // Empty signature - device lacks private key
		})

	case *GetiPodAuthenticationInfo:
		ipod.Respond(req, tr, &RetiPodAuthenticationInfo{
			Major: 1, Minor: 1,
			CertCurrentSection: 0, CertMaxSection: 0, CertData: []byte{},
		})

	case *AckiPodAuthenticationInfo:
		// pass

	case *GetiPodAuthenticationSignature:
		ipod.Respond(req, tr, &RetiPodAuthenticationSignature{Signature: msg.Challenge})

	case *AckiPodAuthenticationStatus:
		// pass

	// revisit
	case *GetiPodOptions:
		ipod.Respond(req, tr, &RetiPodOptions{Options: 0x00})

	// GetAccessoryInfo
	// check back might be useful
	case *RetAccessoryInfo:
		// pass

	case *GetiPodPreferences:
		ipod.Respond(req, tr, &RetiPodPreferences{
			PrefClassID:        msg.PrefClassID,
			PrefClassSettingID: dev.PrefSettingID(msg.PrefClassID),
		})

	case *SetiPodPreferences:
		dev.SetPrefSettingID(msg.PrefClassID, msg.PrefClassSettingID, ipod.ByteToBool(msg.RestoreOnExit))
		ipod.Respond(req, tr, ackSuccess(req))

	case *GetUIMode:
		ipod.Respond(req, tr, &RetUIMode{UIMode: dev.UIMode()})
	case *SetUIMode:
		dev.SetUIMode(msg.UIMode)
		ipod.Respond(req, tr, ackSuccess(req))

	case *StartIDPS:
		ipod.TrxReset()
		dev.StartIDPS()
		ipod.Respond(req, tr, ackSuccess(req))
	case *SetFIDTokenValues:
		for _, token := range msg.FIDTokenValues {
			dev.SetToken(token)
		}
		ipod.Respond(req, tr, ackFIDTokenValues(msg))
	case *EndIDPS:
		log.WithField("status", msg.AccEndIDPSStatus).Info("[AUTH] EndIDPS received")
		dev.EndIDPS(msg.AccEndIDPSStatus)
		switch msg.AccEndIDPSStatus {
		case AccEndIDPSStatusContinue:
			log.Info("[AUTH] Responding with IDPSStatus OK")
			ipod.Respond(req, tr, &IDPSStatus{Status: IDPSStatusOK})
			// Initiate authentication by requesting accessory's auth info
			log.Info("[AUTH] Initiating authentication - sending GetDevAuthenticationInfo to car")
			ipod.Send(tr, &GetDevAuthenticationInfo{})
		case AccEndIDPSStatusReset:
			log.Info("[AUTH] EndIDPS Reset status")
			ipod.Respond(req, tr, &IDPSStatus{Status: IDPSStatusTimeLimitNotExceeded})
		case AccEndIDPSStatusAbandon:
			log.Warn("[AUTH] EndIDPS Abandon status")
			ipod.Respond(req, tr, &IDPSStatus{Status: IDPSStatusWillNotAccept})
		case AccEndIDPSStatusNewLink:
			log.Info("[AUTH] EndIDPS NewLink status")
			//pass
		}

	// SetAccStatusNotification, RetAccStatusNotification
	case *AccessoryStatusNotification:

	// iPodNotification later
	case *SetEventNotification:
		dev.SetEventNotificationMask(msg.EventMask)
		ipod.Respond(req, tr, ackSuccess(req))

	case *GetiPodOptionsForLingo:
		ipod.Respond(req, tr, &RetiPodOptionsForLingo{
			LingoID: msg.LingoID,
			Options: dev.LingoOptions(msg.LingoID),
		})

	case *GetEventNotification:
		ipod.Respond(req, tr, &RetEventNotification{
			EventMask: dev.EventNotificationMask(),
		})

	case *GetSupportedEventNotification:
		ipod.Respond(req, tr, &RetSupportedEventNotification{
			EventMask: dev.SupportedEventNotificationMask(),
		})

	case *CancelCommand:
		dev.CancelCommand(msg.LingoID, msg.CmdID, msg.TransactionID)
		ipod.Respond(req, tr, ackSuccess(req))

	case *SetAvailableCurrent:
		// Car tells us the available current (power draw)
		// Acknowledge it
		ipod.Respond(req, tr, ackSuccess(req))

	case *RequestApplicationLaunch:
		ipod.Respond(req, tr, ack(req, ACKStatusFailed))

	case *GetNowPlayingFocusApp:
		ipod.Respond(req, tr, &RetNowPlayingFocusApp{AppID: ipod.StringToBytes("")})

	case ipod.UnknownPayload:
		ipod.Respond(req, tr, ack(req, ACKStatusUnkownID))
	default:
		_ = msg
	}
	return nil
}
