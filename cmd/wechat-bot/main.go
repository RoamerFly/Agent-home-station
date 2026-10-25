// wechat-bot handles allowlisted native text events without OpenClaw.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/linuxnative"
)

func main() {
	if err := run(); err != nil {
		log.Print("wechat-bot stopped: " + err.Error())
		os.Exit(1)
	}
}
func run() error {
	check := flag.Bool("check-model", false, "make one synthetic model connectivity check; no WeChat send")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	model, err := deepseek.New(deepseek.Config{BaseURL: os.Getenv("DEEPSEEK_BASE_URL"), KeyFile: os.Getenv("DEEPSEEK_API_KEY_FILE"), Model: os.Getenv("DEEPSEEK_MODEL")})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *check {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		_, err := model.Complete(ctx, "请只回复：连接成功。")
		if err != nil {
			return err
		}
		fmt.Println(`{"model_request_succeeded":true,"wechat_message_sent":false}`)
		return nil
	}
	sender, err := linuxnative.New(linuxnative.Config{URL: os.Getenv("WECHAT_NATIVE_URL"), TokenFile: os.Getenv("WECHAT_NATIVE_TOKEN_FILE"), Account: os.Getenv("WECHAT_NATIVE_ACCOUNT")})
	if err != nil {
		return err
	}
	if !sender.Ready() {
		log.Print("waiting_for_native_login")
		for !sender.Ready() {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
		}
	}
	worker, err := bot.New(bot.Config{StateDir: os.Getenv("WECHAT_BOT_STATE_DIR"), AllowedThreads: strings.Split(os.Getenv("WECHAT_BOT_ALLOWED_THREADS"), ","), Prefix: os.Getenv("WECHAT_BOT_PREFIX"), Log: func(event string) { log.Print(event) }}, sender, model, sender)
	if err != nil {
		return err
	}
	return worker.Run(ctx)
}
