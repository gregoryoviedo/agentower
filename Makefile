.PHONY: app build icons clean

ROOT := $(shell pwd)
APP_DIR := $(ROOT)/macos/Agentower

app: build
	@echo ""
	@echo "✓ App lista en: $(ROOT)/dist/Agentower.app"
	@echo ""
	@echo "Para instalar:"
	@echo "    cp -R $(ROOT)/dist/Agentower.app /Applications/"
	@echo ""
	@echo "Para abrir (primera vez desbloquea Gatekeeper):"
	@echo "    xattr -dr com.apple.quarantine $(ROOT)/dist/Agentower.app"
	@echo "    open $(ROOT)/dist/Agentower.app"

build:
	bash $(APP_DIR)/scripts/build.sh

icons:
	bash $(APP_DIR)/scripts/make-icon.sh

clean:
	rm -rf $(APP_DIR)/build $(APP_DIR)/Agentower.xcodeproj $(ROOT)/dist
	rm -rf $(APP_DIR)/Agentower/Resources/remote-bot