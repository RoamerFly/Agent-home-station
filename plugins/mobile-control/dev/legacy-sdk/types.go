package mobile

import "wechat-bot/internal/devicespec"

// Compatibility aliases retain SDK source and wire identity. Production hosts
// import devicespec directly and construct their installed workflow adapter.
type Settings = devicespec.Settings
type Source = devicespec.Source
type Event = devicespec.Event
type Task = devicespec.Task
type Artifact = devicespec.Artifact
type ArtifactAnalyzer = devicespec.ArtifactAnalyzer
type Observation = devicespec.Observation
type Step = devicespec.Step
type Planner = devicespec.Planner
type Bridge = devicespec.Bridge
type BrokerFactory = devicespec.BrokerFactory

func Defaults() Settings { return devicespec.Defaults() }
