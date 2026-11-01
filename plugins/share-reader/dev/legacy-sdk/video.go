// Package channels handles the prefix transform for video bytes whose decode
// key was supplied by the same received WeChat Channels card.
package channels

import "wechat-bot/internal/mediadecode"

// DecodePrefix modifies only the first 128 KiB, including short inputs. It does
// not authenticate the result: callers must validate it as playable media.
func DecodePrefix(data []byte, key uint64) {
	mediadecode.DecodeChannelsPrefix(data, key)
}
