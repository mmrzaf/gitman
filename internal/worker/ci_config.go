package worker

import cipipeline "github.com/mmrzaf/gitman/internal/ci"

const ciConfigFile = cipipeline.ConfigFile

type CIConfig = cipipeline.Config
type CIStep = cipipeline.Step
type envEntry = cipipeline.EnvEntry

var parseCIConfig = cipipeline.ParseConfig
