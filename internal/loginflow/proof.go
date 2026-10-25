package loginflow

import (
	"errors"
	"math/rand"
	"time"

	"wechat-bot/internal/authwire"
	"wechat-bot/internal/deviceprofile"
	"wechat-bot/internal/deviceproof"
	"wechat-bot/internal/sae08"
)

// AuthorizationProof combines the recovered producer chain with an actual
// server-issued device token. Clock and random inputs make each sampling step
// explicit. No token generation or network call occurs here.
func AuthorizationProof(profile deviceprofile.Profile, serverToken, username string, cipher *sae08.Cipher, clock func() time.Time, random *rand.Rand) ([]byte, error) {
	if clock == nil || random == nil || cipher == nil || serverToken == "" || username == "" || len(username) > deviceproof.MaxRunningCount || profile.LoginDeviceID == "" {
		return nil, errors.New("authorization proof requires profile, actual token, username, clock and random source")
	}
	deltas := make([]uint32, len(username))
	for i := range deltas {
		deltas[i] = uint32(random.Int63n(10000))
	}
	wcstf, err := deviceproof.BuildWCSTF(clock(), uint32(len(username)), deltas, cipher)
	if err != nil {
		return nil, err
	}
	seconds, millis := clock(), clock()
	wcste, err := deviceproof.BuildWCSTEAt(seconds, millis, cipher)
	if err != nil {
		return nil, err
	}
	body, err := deviceproof.BuildSpamBody(deviceproof.SpamInput{Profile: profile, LoginDeviceID: profile.LoginDeviceID, DeviceInfoName: profile.DeviceName, RomModel: profile.Model, StartTime: clock(), NowTime: clock(), ServerDeviceToken: serverToken, Build: deviceproof.SelectedSpamBuild()})
	if err != nil {
		return nil, err
	}
	check, err := deviceproof.MarshalSpamCheckData(body, uint32(clock().Unix()))
	if err != nil {
		return nil, err
	}
	spam, err := deviceproof.BuildRunningInfo(check, uint32(clock().Unix()), cipher)
	if err != nil {
		return nil, err
	}
	return authwire.BuildExtInfoV8(authwire.ExtInfoV8Parts{WCSTF: wcstf, WCSTE: wcste, EncryptedDeviceData: spam, DeviceToken: serverToken, CryptoTimestamp: uint32(clock().Unix()), TokenTimestamp: uint32(clock().Unix())})
}
