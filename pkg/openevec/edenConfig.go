package openevec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lf-edge/eden/pkg/defaults"
	"github.com/lf-edge/eden/pkg/models"
	"github.com/lf-edge/eden/pkg/utils"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// optionsWithMapTypeList contains options that should be encoded
var optionsWithMapTypeList = []string{"eve.hostfwd"}

func isEncodingNeeded(contextKeySet string) bool {
	for _, k := range optionsWithMapTypeList {
		if contextKeySet != k {
			continue
		}
		return true
	}
	return false
}

func ReloadConfigDetails(cfg *EdenSetupArgs) error {
	viperLoaded, err := utils.LoadConfigFile(cfg.ConfigFile)
	if err != nil {
		return fmt.Errorf("error reading config: %w", err)
	}
	if viperLoaded {
		cfg.Eve.QemuFirmware = viper.GetStringSlice("eve.firmware")
		cfg.Eve.QemuConfigPath = utils.ResolveAbsPath(viper.GetString("eve.config-part"))
		cfg.Eve.QemuDTBPath = utils.ResolveAbsPath(viper.GetString("eve.dtb-part"))
		cfg.Eve.ImageFile = utils.ResolveAbsPath(viper.GetString("eve.image-file"))
		cfg.Eve.HostFwd = viper.GetStringMapString("eve.hostfwd")
		cfg.Eve.QemuFileToSave = utils.ResolveAbsPath(viper.GetString("eve.qemu-config"))
		cfg.Eve.DevModel = viper.GetString("eve.devmodel")
		cfg.Eve.Remote = viper.GetBool("eve.remote")
		cfg.Eve.ModelFile = viper.GetString("eve.devmodelfile")
		if cfg.Eve.ModelFile != "" {
			filePath, err := filepath.Abs(cfg.Eve.ModelFile)
			if err != nil {
				return fmt.Errorf("cannot get absolute path for devmodelfile (%s): %w", cfg.Eve.ModelFile, err)
			}
			if _, err := os.Stat(filePath); err != nil {
				return fmt.Errorf("cannot parse devmodelfile (%s): %w", cfg.Eve.ModelFile, err)
			}
		}
	}
	return nil
}

func ConfigAdd(cfg *EdenSetupArgs, currentContext, contextFile string, force bool) error {
	var err error
	if cfg.ConfigFile == "" {
		cfg.ConfigFile, err = utils.DefaultConfigPath()
		if err != nil {
			return fmt.Errorf("fail in DefaultConfigPath: %w", err)
		}
	}
	model, err := models.GetDevModelByName(cfg.Eve.DevModel)
	if err != nil {
		return fmt.Errorf("GetDevModelByName: %w", err)
	}
	if _, err := os.Stat(cfg.ConfigFile); !os.IsNotExist(err) {
		if force {
			if err := os.Remove(cfg.ConfigFile); err != nil {
				return err
			}
		} else {
			log.Debugf("current config already exists: %s", cfg.ConfigFile)
		}
	}
	if _, err = os.Stat(cfg.ConfigFile); os.IsNotExist(err) {
		if err = utils.GenerateConfigFile(cfg.ConfigFile); err != nil {
			return fmt.Errorf("fail in generate yaml: %w", err)
		}
		log.Infof("Config file generated: %s", cfg.ConfigFile)
	}
	if err := ReloadConfigDetails(cfg); err != nil {
		return err
	}

	context, err := utils.ContextLoad()
	if err != nil {
		return fmt.Errorf("load context error: %w", err)
	}
	currentContextName := context.Current
	if currentContext != "" {
		context.Current = currentContext
	} else {
		context.Current = "default"
	}
	cfg.ConfigFile = context.GetCurrentConfig()
	if contextFile != "" {
		if err := utils.CopyFile(contextFile, cfg.ConfigFile); err != nil {
			return fmt.Errorf("cannot copy file: %w", err)
		}
		log.Infof("Context file generated: %s", contextFile)
	} else {
		if _, err := os.Stat(cfg.ConfigFile); os.IsNotExist(err) {
			if err = utils.GenerateConfigFileDiff(cfg.ConfigFile, context); err != nil {
				return fmt.Errorf("error generate config: %w", err)
			}
			log.Infof("Context file generated: %s", cfg.ConfigFile)
		} else {
			log.Infof("Config file already exists %s", cfg.ConfigFile)
		}
	}
	context.SetContext(context.Current)
	if err := ReloadConfigDetails(cfg); err != nil {
		return err
	}

	// we prepare viper config here from EdenSetupArgs
	// to feed into GenerateConfigFileFromViper

	if cfg.Eve.Arch != "" {
		viper.Set("eve.arch", cfg.Eve.Arch)
		imageDist := fmt.Sprintf("%s-%s", context.Current, defaults.DefaultImageDist)
		switch cfg.Eve.Arch {
		case "amd64":
			viper.Set("eve.firmware", []string{filepath.Join(imageDist, "eve", "OVMF_CODE.fd"),
				filepath.Join(imageDist, "eve", "OVMF_VARS.fd")})
		case "arm64":
			viper.Set("eve.firmware", []string{filepath.Join(imageDist, "eve", "OVMF.fd")})
		}
	}
	if cfg.Eve.Ssid != "" {
		viper.Set("eve.ssid", cfg.Eve.Ssid)
	}

	for k, v := range model.Config() {
		viper.Set(k, v)
	}

	if err = utils.GenerateConfigFileFromViper(); err != nil {
		return fmt.Errorf("error writing config: %w", err)
	}
	context.SetContext(currentContextName)

	return nil
}

func ConfigList() error {
	context, err := utils.ContextLoad()
	if err != nil {
		return fmt.Errorf("load context error: %w", err)
	}
	currentContext := context.Current
	contexts := context.ListContexts()
	for _, el := range contexts {
		if el == currentContext {
			fmt.Println("* " + el)
		} else {
			fmt.Println(el)
		}
	}
	return nil
}

func ValidateConfigFromViper() error {
	cfg := &EdenSetupArgs{}
	if err := viper.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unable to decode into config struct, %w", err)
	}
	return nil
}

func processConfigKeyValue(contextKeySet, contextValueSet string) (interface{}, error) {
	if isEncodingNeeded(contextKeySet) {
		obj := make(map[string]interface{})
		err := json.Unmarshal([]byte(contextValueSet), &obj)
		if err != nil {
			return nil, fmt.Errorf("failed to decode %s: %s", contextKeySet, err)
		}
		return obj, nil
	}
	return contextValueSet, nil
}

func ConfigSet(target, contextKeySet, contextValueSet string) error {
	context, err := utils.ContextLoad()
	if err != nil {
		return fmt.Errorf("load context error: %w", err)
	}
	oldContext := context.Current
	if contextKeySet != "" {
		defer context.SetContext(oldContext) // restore context after modifications
	}
	objToStore, err := processConfigKeyValue(contextKeySet, contextValueSet)
	if err != nil {
		return fmt.Errorf("processConfigKeyValue error: %w", err)
	}
	contexts := context.ListContexts()
	for _, el := range contexts {
		if el == target {
			context.SetContext(el)
			if contextKeySet != "" {
				_, err := utils.LoadConfigFileContext(context.GetCurrentConfig())
				if err != nil {
					return fmt.Errorf("error reading config: %w", err)
				}
				viper.Set(contextKeySet, objToStore)
				if err = ValidateConfigFromViper(); err != nil {
					return fmt.Errorf("ValidateConfigFromViper: %w", err)
				}
				if err = utils.GenerateConfigFileFromViper(); err != nil {
					return fmt.Errorf("error writing config: %w", err)
				}
			}
			log.Infof("Current context is: %s", el)
			return nil
		}
	}
	return fmt.Errorf("context not found %s", target)
}

func ConfigEdit(target string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return fmt.Errorf("$EDITOR environment not set")
	}
	context, err := utils.ContextLoad()
	if err != nil {
		return fmt.Errorf("load context error: %w", err)
	}

	contextNameEdit := context.Current
	if target != "" {
		contextNameEdit = target
	}
	contexts := context.ListContexts()
	for _, el := range contexts {
		if el == contextNameEdit {
			context.Current = contextNameEdit
			if err = utils.RunCommandForeground(editor, context.GetCurrentConfig()); err != nil {
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("context not found %s", contextNameEdit)
}

func ConfigReset(target string) error {
	context, err := utils.ContextLoad()
	if err != nil {
		return fmt.Errorf("load context error: %w", err)
	}
	oldContext := context.Current
	defer context.SetContext(oldContext) // restore context after modifications

	contextNameReset := context.Current
	if target != "" {
		contextNameReset = target
	}
	contexts := context.ListContexts()
	for _, el := range contexts {
		if el == contextNameReset {
			context.SetContext(el)
			if err = os.Remove(context.GetCurrentConfig()); err != nil {
				return fmt.Errorf("cannot delete old config file: %w", err)
			}
			_, err := utils.LoadConfigFile(context.GetCurrentConfig())
			if err != nil {
				return fmt.Errorf("error reading config: %w", err)
			}
			return nil
		}
	}
	return fmt.Errorf("context not found %s", contextNameReset)
}

func ConfigGet(target string, contextKeyGet string, contextAllGet bool) error {
	context, err := utils.ContextLoad()
	if err != nil {
		return fmt.Errorf("load context error: %w", err)
	}
	oldContext := context.Current
	defer context.SetContext(oldContext) // restore context after modifications
	if target != "" {
		found := false
		contexts := context.ListContexts()
		for _, el := range contexts {
			if el == target {
				context.SetContext(el)
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("context not found %s", target)
		}
		_, err := utils.LoadConfigFile(context.GetCurrentConfig())
		if err != nil {
			return fmt.Errorf("error reading config: %w", err)
		}
	}
	switch {
	case contextKeyGet == "" && !contextAllGet:
		fmt.Println(context.Current)
	case contextKeyGet != "":
		item := viper.Get(contextKeyGet)
		if isEncodingNeeded(contextKeyGet) {
			result, err := json.Marshal(item)
			if err != nil {
				return err
			}
			fmt.Println(string(result))
		} else {
			fmt.Println(item)
		}
	case contextAllGet:
		if err = viper.WriteConfigAs(defaults.DefaultConfigHidden); err != nil {
			return err
		}
		data, err := os.ReadFile(defaults.DefaultConfigHidden)
		if err != nil {
			return fmt.Errorf("cannot read context config file %s: %s", target, err)
		}
		fmt.Print(string(data))
	}
	return nil
}

func ConfigDelete(target string, cfg *EdenSetupArgs) error {
	context, err := utils.ContextLoad()
	if err != nil {
		return fmt.Errorf("load context error: %w", err)
	}
	currentContext := context.Current
	log.Infof("currentContext %s", currentContext)
	log.Infof("contextName %s", target)
	if (target == "" || target == defaults.DefaultContext) && defaults.DefaultContext == currentContext {
		return fmt.Errorf("cannot delete default context. Use 'eden clean' instead")
	}
	if target == currentContext {
		target = context.Current
		context.SetContext(defaults.DefaultContext)
		log.Infof("Move to %s context", defaults.DefaultContext)
	}
	context.Current = target
	configFile := context.GetCurrentConfig()
	if err := ReloadConfigDetails(cfg); err != nil {
		log.Infof("Failed to ReloadConfigDetails: %s", err.Error())
	}
	if _, err := os.Stat(cfg.Eve.QemuFileToSave); !os.IsNotExist(err) {
		if err := os.Remove(cfg.Eve.QemuFileToSave); err == nil {
			log.Infof("deleted qemu config %s", cfg.Eve.QemuFileToSave)
		}
	}
	log.Infof("currentContextFile %s", configFile)
	if err := os.Remove(configFile); err != nil {
		return fmt.Errorf("cannot delete context %s: %s", target, err)
	}
	return nil
}
