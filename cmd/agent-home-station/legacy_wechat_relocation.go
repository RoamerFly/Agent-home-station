package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"wechat-bot/internal/station"
)

func runLegacyWechatRelocation(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("legacy-wechat-relocation", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var request station.LegacyWechatRelocationRequest
	flags.StringVar(&request.DataDir, "data", "", "目标站数据绝对目录")
	flags.StringVar(&request.SourceDir, "source", "", "原站数据绝对目录；不提供时只准备目标标识")
	flags.StringVar(&request.ConnectionID, "connection", "", "原微信连接标识")
	flags.StringVar(&request.AuditDigest, "audit-digest", "", "封存时使用的原站投递核对摘要")
	flags.StringVar(&request.ApplyDigest, "apply", "", "使用预检令牌固定旧投递归档，连接保持冻结")
	flags.BoolVar(&request.AcceptUnknown, "accept-unknown", false, "沿用原站封存时对未知结果的确认")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("微信搬迁核验不接受额外参数")
	}
	result, err := station.CheckLegacyWechatRelocation(ctx, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
