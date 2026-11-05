package admin

import (
	"context"
	"encoding/json"
	"errors"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

const musicPreparationCapability = "music.prepare"

// Pure package computations reuse the same current source, account, package,
// configuration and task proof as the observed bridge resource. They cannot
// call a host service. The caller validates only authority-bearing output.
type musicPreparationPolicy struct {
	base      *localMusicPluginPolicy
	arguments json.RawMessage
	output    any
	verify    func() error
}

func (p *musicPreparationPolicy) Check(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability) error {
	return p.base.checkBoundCall(ctx, j, cap, musicPreparationCapability, p.arguments)
}

func (p *musicPreparationPolicy) Validate(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, raw json.RawMessage) (pluginruntime.VerifiedResult, error) {
	if err := p.Check(ctx, j, cap); err != nil {
		return pluginruntime.VerifiedResult{}, err
	}
	var failure struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if musicStrictJSON(raw, &failure) == nil && failure.OK != nil && !*failure.OK && localMusicSafeCode(failure.Error) {
		return pluginruntime.VerifiedResult{}, toolInputError(failure.Error)
	}
	if len(raw) > 512<<10 || musicStrictJSON(raw, p.output) != nil {
		return pluginruntime.VerifiedResult{}, errors.New("music_preparation_result_invalid")
	}
	if p.verify != nil {
		if err := p.verify(); err != nil {
			return pluginruntime.VerifiedResult{}, err
		}
	}
	return pluginruntime.VerifiedResult{Result: append(json.RawMessage(nil), raw...)}, nil
}

func (p *permittedTools) prepareMusic(ctx context.Context, observedPath string, input, output any, verify func() error) error {
	base, err := p.newLocalMusicPluginPolicy(ctx, "GET", observedPath, nil, "")
	if err != nil {
		return err
	}
	args, err := json.Marshal(input)
	if err != nil {
		return err
	}
	policy := &musicPreparationPolicy{base: base, arguments: args, output: output, verify: verify}
	c, ready, err := p.manager.installedPluginRuntime()
	if err != nil || !ready {
		return errors.New("music_preparation_runtime_unavailable")
	}
	i, err := base.store.Installed(ctx, "music")
	if err != nil {
		return err
	}
	if err = c.EnsureStarted(ctx, "music", i.Revision); err != nil {
		return err
	}
	call, err := c.Invoke(ctx, pluginruntime.CallRequest{Plugin: "music", Capability: musicPreparationCapability, Context: base.invocation, Arguments: args}, policy)
	if err != nil {
		return err
	}
	if call.Job.State != "completed" {
		return errors.New("music_preparation_needs_review")
	}
	return policy.Check(ctx, call.Job, pluginhost.Capability{})
}
