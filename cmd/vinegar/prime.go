package main

import (
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/vinegarhq/vinegar/internal/config"
)

type Card struct {
	path string
	eDP  bool
	id   string
}

//Note: sysfs is located entirely in memory, and as a result does not have IO errors.
//As a result, no error handling when calling os IO operations is done.

func ChooseCard(bcfg config.Binary, c Card) config.Binary {
	setIfUndefined := func(k string, v string) {
		log.Printf("bcfg.Env: %v", bcfg.Env)
		if _, ok := bcfg.Env[k]; ok {
			log.Printf("Warning: env var %s is already defined. Will not redefine it.", k)
		} else {
			bcfg.Env[k] = v
		}
	}

	setIfUndefined("MESA_VK_DEVICE_SELECT_FORCE_DEFAULT_DEVICE", "1")
	setIfUndefined("DRI_PRIME", c.id)

	driverPath, _ := filepath.EvalSymlinks(filepath.Join(c.path, "device/driver"))

	//Nvidia proprietary driver is being used. Apply workaround.
	if strings.HasSuffix(driverPath, "nvidia") {
		setIfUndefined("__GLX_VENDOR_LIBRARY_NAME", "nvidia")
	} else {
		setIfUndefined("__GLX_VENDOR_LIBRARY_NAME", "mesa")
	}

	log.Printf("Chose card %s (%s).", c.path, c.id)
	return bcfg
}

// Probe cards of system and their properties via sysfs
func GetSystemCards() ([]*Card, map[string]*Card) {
	cardPattern := regexp.MustCompile("card([0-9]+)$")
	eDP := regexp.MustCompile("card([0-9]+)-eDP-[0-9]+$")
	drmPath := "/sys/class/drm"

	var cards = make([]*Card, 0)
	idDict := make(map[string]*Card, 100)

	dirEntries, _ := os.ReadDir(drmPath)

	for _, v := range dirEntries {
		name := v.Name()
		submatch := cardPattern.FindStringSubmatch(name)
		eDPSubmatch := eDP.FindStringSubmatch(name)

		if submatch != nil {
			i, _ := strconv.Atoi(submatch[1])

			cardPath := path.Join(drmPath, name)

			card := new(Card)
			cards = append(cards, card)

			cards[i].path = cardPath
			vid, _ := os.ReadFile(path.Join(cardPath, "device/vendor"))
			did, _ := os.ReadFile(path.Join(cardPath, "device/device"))

			vidCut, _ := strings.CutPrefix(string(vid), "0x")
			didCut, _ := strings.CutPrefix(string(did), "0x")

			id := strings.ReplaceAll(strings.ToLower(vidCut+":"+didCut), "\n", "")
			cards[i].id = id
			idDict[id] = cards[i]

		} else if eDPSubmatch != nil {
			i, _ := strconv.Atoi(eDPSubmatch[0])
			cards[i].eDP = true
		}
	}
	return cards, idDict
}

// Check if the system actually has PRIME offload and there's no ambiguity with the GPUs.
func PrimeIsAllowed(cards []*Card, bcfg config.Binary) bool {
	//There's no ambiguity when there's only one card.
	if len(cards) <= 1 {
		log.Printf("Number of cards is equal or below 1. Skipping prime logic.")
		return false
	}
	//Handle exotic systems with three or more GPUs (Usually laptops with an epu connnected or workstation desktops)
	if len(cards) > 2 {
		//OpenGL cannot choose the right card properly. Prompt user the define it themselves
		if !bcfg.Dxvk && bcfg.Renderer != "Vulkan" {
			log.Printf("System has %d cards and OpenGL is not capable of choosing the right one.", len(cards))
			log.Printf("To fix this, use the Vulkan renderer instead or choose the right GPU in Vinegar's configuration.")
			log.Printf("Aborting...")
			os.Exit(1)
		} else { //Vulkan knows better than us. Let it do its thing.
			log.Printf("System has %d cards. Skipping prime logic and leaving card selection up to Vulkan.", len(cards))
		}
		return false
	}
	//card0 is always an igpu if it exists. If it has no eDP, then Vinegar isn't running on a laptop.
	//As a result, prime doesn't exist and should be skipped.
	if !cards[0].eDP {
		log.Printf("card0 has no eDP. This machine is not a laptop. Skipping prime logic.")
		return false
	}
	return true
}

func SetupPrimeOffload(bcfg config.Binary) config.Binary {
	//This allows the user to skip PrimeOffload logic. Useful if they want to take care of it themselves.
	if bcfg.ForcedGpu == "" {
		log.Printf("ForcedGpu option is empty. Skipping prime logic...")
		return bcfg
	}

	cards, idDict := GetSystemCards()

	switch bcfg.ForcedGpu {
	case "integrated":
		if !PrimeIsAllowed(cards, bcfg) {
			return bcfg
		}
		return ChooseCard(bcfg, *cards[0])
	case "prime-discrete":
		if !PrimeIsAllowed(cards, bcfg) {
			return bcfg
		}
		return ChooseCard(bcfg, *cards[1])

	//Handle cases where the user explictly chooses a gpu to use
	default:
		if strings.Contains(bcfg.ForcedGpu, ":") { //This is a card vid:nid
			card := idDict[bcfg.ForcedGpu]
			if card == nil {
				log.Printf("No gpu with the vid:nid \"%s\". Aborting.", bcfg.ForcedGpu)
				os.Exit(1)
			}
			return ChooseCard(bcfg, *card)
		} else { // This is an index
			id, _ := strconv.Atoi(bcfg.ForcedGpu)
			card := cards[id]
			if card == nil {
				log.Printf("index %d of ForcedGpu does not exist. Aborting.", id)
				os.Exit(1)
			}
			return ChooseCard(bcfg, *card)
		}
	}
}
