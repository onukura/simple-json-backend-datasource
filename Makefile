all: webpack build
deps: deps-front deps-back

webpack:
	webpack --config=./webpack/webpack.dev.conf.js

build:
	mage -v

deps-front:
	yarn install --pure-lockfile

deps-back:
	go mod tidy