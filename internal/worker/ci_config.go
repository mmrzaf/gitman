package worker

import cipipeline "github.com/mmrzaf/gitman/internal/ci"

const ciConfigFile = cipipeline.ConfigFile

type CIConfig = cipipeline.Config

var parseCIConfig = cipipeline.ParseConfig
