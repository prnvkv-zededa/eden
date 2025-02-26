package openevec

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lf-edge/eden/pkg/controller"
	"github.com/lf-edge/eden/pkg/controller/types"
	"github.com/lf-edge/eden/pkg/defaults"
	"github.com/lf-edge/eden/pkg/expect"
	"github.com/lf-edge/eden/pkg/utils"
	"github.com/lf-edge/eve/api/go/config"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func EdgeNodeReboot(controllerMode string) error {
	changer, err := changerByControllerMode(controllerMode)
	if err != nil {
		return err
	}
	ctrl, dev, err := changer.getControllerAndDev()
	if err != nil {
		return fmt.Errorf("getControllerAndDev error: %w", err)
	}
	dev.Reboot()
	if err = changer.setControllerAndDev(ctrl, dev); err != nil {
		return fmt.Errorf("setControllerAndDev error: %w", err)
	}
	log.Info("Reboot request has been sent")

	return nil
}

func EdgeNodeShutdown(controllerMode string) error {
	changer, err := changerByControllerMode(controllerMode)
	if err != nil {
		return err
	}
	ctrl, dev, err := changer.getControllerAndDev()
	if err != nil {
		return fmt.Errorf("getControllerAndDev error: %w", err)
	}
	dev.Shutdown()
	if err = changer.setControllerAndDev(ctrl, dev); err != nil {
		return fmt.Errorf("setControllerAndDev error: %w", err)
	}
	log.Info("Shutdown request has been sent")

	return nil
}

func EdgeNodeEVEImageUpdate(baseOSImage, baseOSVersion, registry, controllerMode string,
	baseOSImageActivate, baseOSVDrive bool) error {

	var opts []expect.ExpectationOption
	changer, err := changerByControllerMode(controllerMode)
	if err != nil {
		return err
	}
	ctrl, dev, err := changer.getControllerAndDev()
	if err != nil {
		return fmt.Errorf("getControllerAndDev error: %w", err)
	}
	registryToUse := registry
	switch registry {
	case "local":
		registryToUse = fmt.Sprintf("%s:%d", viper.GetString("registry.ip"), viper.GetInt("registry.port"))
	case "remote":
		registryToUse = ""
	}
	opts = append(opts, expect.WithRegistry(registryToUse))
	expectation := expect.AppExpectationFromURL(ctrl, dev, baseOSImage, "", opts...)
	if baseOSVDrive {
		baseOSImageConfig := expectation.BaseOSConfig(baseOSVersion)
		dev.SetBaseOSConfig(append(dev.GetBaseOSConfigs(), baseOSImageConfig.Uuidandversion.Uuid))
	}

	baseOS := expectation.BaseOS(baseOSVersion)
	dev.SetBaseOSActivate(baseOSImageActivate)
	dev.SetBaseOSContentTree(baseOS.ContentTreeUuid)
	dev.SetBaseOSRetryCounter(0)
	dev.SetBaseOSVersion(baseOS.BaseOsVersion)

	if err = changer.setControllerAndDev(ctrl, dev); err != nil {
		return fmt.Errorf("setControllerAndDev: %w", err)
	}
	return nil
}

func EdgeNodeEVEImageUpdateRetry(controllerMode string) error {
	changer, err := changerByControllerMode(controllerMode)
	if err != nil {
		return err
	}
	ctrl, dev, err := changer.getControllerAndDev()
	if err != nil {
		return fmt.Errorf("getControllerAndDev error: %w", err)
	}
	dev.SetBaseOSRetryCounter(dev.GetBaseOSRetryCounter() + 1)

	if err = changer.setControllerAndDev(ctrl, dev); err != nil {
		return fmt.Errorf("setControllerAndDev: %w", err)
	}

	return nil
}

func checkIsFileOrURL(pathToCheck string) (isFile bool, pathToRet string, err error) {
	res, err := url.Parse(pathToCheck)
	if err != nil {
		return false, "", err
	}
	switch res.Scheme {
	case "":
		return true, pathToCheck, nil
	case "file":
		return true, strings.TrimPrefix(pathToCheck, "file://"), nil
	case "http":
		return false, pathToCheck, nil
	case "https":
		return false, pathToCheck, nil
	case "oci":
		return false, pathToCheck, nil
	case "docker":
		return false, pathToCheck, nil
	default:
		return false, "", fmt.Errorf("%s scheme not supported now", res.Scheme)
	}
}

func EdgeNodeEVEImageRemove(controllerMode, baseOSVersion, baseOSImage, edenDist string) error {
	isFile, baseOSImage, err := checkIsFileOrURL(baseOSImage)
	if err != nil {
		return fmt.Errorf("checkIsFileOrURL: %w", err)
	}
	var rootFsPath string
	if isFile {
		rootFsPath, err = utils.GetFileFollowLinks(baseOSImage)
		if err != nil {
			return fmt.Errorf("GetFileFollowLinks: %w", err)
		}
	} else {
		r, _ := url.Parse(baseOSImage)
		switch r.Scheme {
		case "http", "https":
			if err = os.MkdirAll(filepath.Join(edenDist, "tmp"), 0755); err != nil {
				return fmt.Errorf("cannot create dir for download image %w", err)
			}
			rootFsPath = filepath.Join(edenDist, "tmp", path.Base(r.Path))
			defer os.Remove(rootFsPath)
			if err := utils.DownloadFile(rootFsPath, baseOSImage); err != nil {
				return fmt.Errorf("DownloadFile error: %w", err)
			}
		case "oci", "docker":
			bits := strings.Split(r.Path, ":")
			if len(bits) == 2 {
				rootFsPath = "rootfs-" + bits[1] + ".dummy"
			} else {
				rootFsPath = "latest.dummy"
			}
		default:
			return fmt.Errorf("unknown URI scheme: %s", r.Scheme)
		}
	}
	changer, err := changerByControllerMode(controllerMode)
	if err != nil {
		return err
	}

	ctrl, dev, err := changer.getControllerAndDev()
	if err != nil {
		return fmt.Errorf("getControllerAndDev error: %w", err)
	}

	if baseOSVersion == "" {
		correctionFileName := fmt.Sprintf("%s.ver", rootFsPath)
		if rootFSFromCorrectionFile, err := os.ReadFile(correctionFileName); err == nil {
			baseOSVersion = string(rootFSFromCorrectionFile)
		} else {
			rootFSName := utils.FileNameWithoutExtension(rootFsPath)
			rootFSName = strings.TrimPrefix(rootFSName, "rootfs-")
			re := regexp.MustCompile(defaults.DefaultRootFSVersionPattern)
			if !re.MatchString(rootFSName) {
				return fmt.Errorf("filename of rootfs %s does not match pattern %s", rootFSName, defaults.DefaultRootFSVersionPattern)
			}
			baseOSVersion = rootFSName
		}
	}

	log.Infof("Will use rootfs version %s", baseOSVersion)

	toActivate := true
	for _, baseOSConfig := range ctrl.ListBaseOSConfig() {
		if baseOSConfig.BaseOSVersion == baseOSVersion {
			if ind, found := utils.FindEleInSlice(dev.GetBaseOSConfigs(), baseOSConfig.Uuidandversion.GetUuid()); found {
				configs := dev.GetBaseOSConfigs()
				utils.DelEleInSlice(&configs, ind)
				dev.SetBaseOSConfig(configs)
				log.Infof("EVE base OS image removed with id %s", baseOSConfig.Uuidandversion.GetUuid())
			}
		} else {
			if toActivate {
				toActivate = false
				baseOSConfig.Activate = true // activate another one if exists
			}
		}
	}
	if err = changer.setControllerAndDev(ctrl, dev); err != nil {
		return fmt.Errorf("setControllerAndDev error: %w", err)
	}
	return nil
}

func EdgeNodeUpdate(controllerMode string, deviceItems, configItems map[string]string) error {
	changer, err := changerByControllerMode(controllerMode)
	if err != nil {
		return err
	}

	ctrl, dev, err := changer.getControllerAndDev()
	if err != nil {
		return fmt.Errorf("getControllerAndDev error: %w", err)
	}
	for key, val := range configItems {
		dev.SetConfigItem(key, val)
	}
	for key, val := range deviceItems {
		if err := dev.SetDeviceItem(key, val); err != nil {
			return fmt.Errorf("SetDeviceItem: %w", err)
		}
	}

	if err = changer.setControllerAndDev(ctrl, dev); err != nil {
		return fmt.Errorf("setControllerAndDev error: %w", err)
	}

	return nil
}

func EdgeNodeGetConfig(controllerMode, fileWithConfig string) error {
	changer, err := changerByControllerMode(controllerMode)
	if err != nil {
		return err
	}

	ctrl, dev, err := changer.getControllerAndDev()
	if err != nil {
		return fmt.Errorf("getControllerAndDev error: %w", err)
	}

	res, err := ctrl.GetConfigBytes(dev, true)
	if err != nil {
		return fmt.Errorf("GetConfigBytes error: %w", err)
	}
	if fileWithConfig != "" {
		if err = os.WriteFile(fileWithConfig, res, 0755); err != nil {
			return fmt.Errorf("writeFile: %w", err)
		}
	} else {
		fmt.Println(string(res))
	}
	return nil
}

func EdgeNodeSetConfig(fileWithConfig string) error {
	ctrl, err := controller.CloudPrepare()
	if err != nil {
		return fmt.Errorf("CloudPrepare: %w", err)
	}
	devFirst, err := ctrl.GetDeviceCurrent()
	if err != nil {
		return fmt.Errorf("GetDeviceCurrent error: %w", err)
	}
	devUUID := devFirst.GetID()
	var newConfig []byte
	if fileWithConfig != "" {
		newConfig, err = os.ReadFile(fileWithConfig)
		if err != nil {
			return fmt.Errorf("file reading error: %w", err)
		}
	} else if utils.IsInputFromPipe() {
		newConfig, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("stdin reading error: %w", err)
		}
	} else {
		return fmt.Errorf("please run command with --file or use it with pipe")
	}
	// we should validate config with unmarshal
	var dConfig config.EdgeDevConfig
	if err := protojson.Unmarshal(newConfig, &dConfig); err != nil {
		return fmt.Errorf("cannot unmarshal config: %w", err)
	}
	// Adam expects json type
	cfg, err := proto.Marshal(&dConfig)
	if err != nil {
		return fmt.Errorf("cannot marshal config: %w", err)
	}
	if err = ctrl.ConfigSet(devUUID, cfg); err != nil {
		return fmt.Errorf("ConfigSet: %w", err)
	}
	log.Info("Config loaded")
	return nil
}

func EdgeNodeGetOptions(controllerMode, fileWithConfig string) error {
	changer, err := changerByControllerMode(controllerMode)
	if err != nil {
		return err
	}
	ctrl, dev, err := changer.getControllerAndDev()
	if err != nil {
		return fmt.Errorf("getControllerAndDev error: %w", err)
	}
	res, err := ctrl.GetDeviceOptions(dev.GetID())
	if err != nil {
		return fmt.Errorf("GetDeviceOptions error: %w", err)
	}
	data, err := json.MarshalIndent(res, "", "    ")
	if err != nil {
		return fmt.Errorf("cannot marshal: %w", err)
	}
	if fileWithConfig != "" {
		if err = os.WriteFile(fileWithConfig, data, 0755); err != nil {
			return fmt.Errorf("WriteFile: %w", err)
		}
	} else {
		fmt.Println(string(data))
	}

	return nil
}

func EdgeNodeSetOptions(controllerMode, fileWithConfig string) error {
	changer, err := changerByControllerMode(controllerMode)
	if err != nil {
		return err
	}
	ctrl, dev, err := changer.getControllerAndDev()
	if err != nil {
		return fmt.Errorf("getControllerAndDev error: %w", err)
	}
	var newOptionsBytes []byte
	if fileWithConfig != "" {
		newOptionsBytes, err = os.ReadFile(fileWithConfig)
		if err != nil {
			return fmt.Errorf("file reading error: %w", err)
		}
	} else if utils.IsInputFromPipe() {
		newOptionsBytes, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("stdin reading error: %w", err)
		}
	} else {
		return fmt.Errorf("please run command with --file or use it with pipe")
	}
	var devOptions types.DeviceOptions
	if err := json.Unmarshal(newOptionsBytes, &devOptions); err != nil {
		return fmt.Errorf("cannot unmarshal: %w", err)
	}
	if err := ctrl.SetDeviceOptions(dev.GetID(), &devOptions); err != nil {
		return fmt.Errorf("cannot set device options: %w", err)
	}
	log.Info("Options loaded")

	return nil
}

func ControllerGetOptions(fileWithConfig string) error {
	ctrl, err := controller.CloudPrepare()
	if err != nil {
		return fmt.Errorf("CloudPrepare error: %w", err)
	}
	res, err := ctrl.GetGlobalOptions()
	if err != nil {
		return fmt.Errorf("GetGlobalOptions error: %w", err)
	}
	data, err := json.MarshalIndent(res, "", "    ")
	if err != nil {
		return fmt.Errorf("cannot marshal: %w", err)
	}
	if fileWithConfig != "" {
		if err = os.WriteFile(fileWithConfig, data, 0755); err != nil {
			return fmt.Errorf("WriteFile: %w", err)
		}
	} else {
		fmt.Println(string(data))
	}
	return nil
}

func ControllerSetOptions(fileWithConfig string) error {
	ctrl, err := controller.CloudPrepare()
	if err != nil {
		return fmt.Errorf("CloudPrepare error: %w", err)
	}
	var newOptionsBytes []byte
	if fileWithConfig != "" {
		newOptionsBytes, err = os.ReadFile(fileWithConfig)
		if err != nil {
			return fmt.Errorf("file reading error: %w", err)
		}
	} else if utils.IsInputFromPipe() {
		newOptionsBytes, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("stdin reading error: %w", err)
		}
	} else {
		return fmt.Errorf("please run command with --file or use it with pipe")
	}
	var globalOptions types.GlobalOptions
	if err := json.Unmarshal(newOptionsBytes, &globalOptions); err != nil {
		return fmt.Errorf("cannot unmarshal: %w", err)
	}
	if err := ctrl.SetGlobalOptions(&globalOptions); err != nil {
		return fmt.Errorf("cannot set global options: %w", err)
	}
	log.Info("Options loaded")

	return nil
}
