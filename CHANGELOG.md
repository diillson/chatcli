# Changelog

## [1.16.3](https://github.com/diillson/chatcli/compare/v1.16.2...v1.16.3) (2025-08-18)


### Bug Fixes

* **modeOneShot:** centralize one-shot handling in `HandleOneShotOrFatal` ([39466e0](https://github.com/diillson/chatcli/commit/39466e0bf16ec5bfb7f9ca2249090a4e35c5f0f0))
* **modeOneShot:** centralize one-shot handling in `HandleOneShotOrFatal` ([17cde1e](https://github.com/diillson/chatcli/commit/17cde1e3fc8b3ac12910b009f6eff28a1abdb224))
* **modeOneShot:** correct stdin example syntax in help message ([993b2ae](https://github.com/diillson/chatcli/commit/993b2ae39fc0a180ec6c9c7bcf5f2923ec27593c))
* **modeOneShot:** streamline input validation and error messaging ([a789298](https://github.com/diillson/chatcli/commit/a789298a36360f7d2bb711613bf4f4f5d07e1136))

## [1.16.2](https://github.com/diillson/chatcli/compare/v1.16.1...v1.16.2) (2025-08-18)


### Bug Fixes

* **modeOneShot:** normalize -p/--prompt flags without values in one-s… ([7ae143b](https://github.com/diillson/chatcli/commit/7ae143b767ca59c217477f69545054d84be57f7b))
* **modeOneShot:** normalize -p/--prompt flags without values in one-shot mode ([10e24da](https://github.com/diillson/chatcli/commit/10e24dafd8a3074f73a07549b1128af1e2e1f745))

## [1.16.1](https://github.com/diillson/chatcli/compare/v1.16.0...v1.16.1) (2025-08-18)


### Bug Fixes

* **modeOneshot:** extend one-shot mode to support stdin input ([999ccdd](https://github.com/diillson/chatcli/commit/999ccdd3d2e36543efb931e81cfca30712e956fb))
* **modeOneshot:** extend one-shot mode to support stdin input ([22acb64](https://github.com/diillson/chatcli/commit/22acb642b5d5f9b4e8c89696ffdc3bb341154cfd))

## [1.16.0](https://github.com/diillson/chatcli/compare/v1.15.0...v1.16.0) (2025-08-18)


### Features

* **cli:** add one-shot mode with prompt flags, provider, and model o… ([bc5b108](https://github.com/diillson/chatcli/commit/bc5b10819f873973302748134848a28b6a87fbb4))
* **cli:** add one-shot mode with prompt flags, provider, and model overrides ([8ff908e](https://github.com/diillson/chatcli/commit/8ff908e03557eaa09a414de406901285b6270cdb))

## [1.15.0](https://github.com/diillson/chatcli/compare/v1.14.1...v1.15.0) (2025-08-16)


### Features

* **cli:** add sensitive data sanitization and enhance configuration … ([66ece6a](https://github.com/diillson/chatcli/commit/66ece6a54e1742116cc1e4456cdfefad021ea5c8))
* **cli:** add sensitive data sanitization and enhance configuration commands ([6b6e9c1](https://github.com/diillson/chatcli/commit/6b6e9c1dbbf031956b117748b10ee7b67c932c58))

## [1.14.1](https://github.com/diillson/chatcli/compare/v1.14.0...v1.14.1) (2025-08-12)


### Bug Fixes

* **version:** correct update command to include 'v' prefix in version string ([518e88e](https://github.com/diillson/chatcli/commit/518e88e5064e835c01802a899dbc98ac3a74a4a1))
* **version:** correct update command to include 'v' prefix in version… ([5a09c42](https://github.com/diillson/chatcli/commit/5a09c426408e0abf026a3d9d8fe8aa054276271d))

## [1.14.0](https://github.com/diillson/chatcli/compare/v1.13.0...v1.14.0) (2025-08-12)


### Features

* refactor message building to avoid prompt duplication and add s… ([92a5c8b](https://github.com/diillson/chatcli/commit/92a5c8b284fe95bb4baa2841a001d57f0f8db5ac))
* refactor message building to avoid prompt duplication and add system-level instructions ([8f140b3](https://github.com/diillson/chatcli/commit/8f140b3035466595bbd6313459aa4627494908bc))


### Bug Fixes

* **gemini:** simplify max tokens assignment logic ([7ae5b9a](https://github.com/diillson/chatcli/commit/7ae5b9a8dc03679cc480a6077f145c0a77e28df8))
* **gemini:** simplify max tokens assignment logic ([954df53](https://github.com/diillson/chatcli/commit/954df532b847f26f1c0dfe7cb43013c1e3dd2f71))

## [1.13.0](https://github.com/diillson/chatcli/compare/v1.12.1...v1.13.0) (2025-08-12)


### Features

* add Google AI (Gemini) provider support and configuration options ([de71481](https://github.com/diillson/chatcli/commit/de7148104530deca8ec4fb26cb083d2b8f40cf51))
* add Google AI (Gemini) provider support and configuration options ([87dbec8](https://github.com/diillson/chatcli/commit/87dbec885058f3d24ad470e2e5e7782f388ea26b))


### Bug Fixes

* **gemini:** handle error when closing response body ([f2d2701](https://github.com/diillson/chatcli/commit/f2d2701b65239cbd8edd8686eecd802f4ae59d6a))
* **gemini:** handle error when closing response body ([14d6622](https://github.com/diillson/chatcli/commit/14d6622fc7c9ff1c43c0e4472930b210e6f7e9ff))

## [1.12.1](https://github.com/diillson/chatcli/compare/v1.12.0...v1.12.1) (2025-08-11)


### Bug Fixes

* **version:** improve version detection and update prompts ([6ed8d2a](https://github.com/diillson/chatcli/commit/6ed8d2a5b0ec493c211b372dd8d8e0cbae4fb23e))
* **version:** improve version detection and update prompts ([e50be13](https://github.com/diillson/chatcli/commit/e50be1324de4e0c5822e631664c62910a429702d))

## [1.12.0](https://github.com/diillson/chatcli/compare/v1.11.0...v1.12.0) (2025-08-09)


### Features

* add support for OpenAI Responses API and enhance configuration options ([334f96e](https://github.com/diillson/chatcli/commit/334f96edf2bff4a013962c21863e5f12e5a68182))
* introduce centralized catalog for model metadata and refactor LLM utilities ([811e00f](https://github.com/diillson/chatcli/commit/811e00ff201604ca08cb2f2b35c37e35713d4296))

## [1.11.0](https://github.com/diillson/chatcli/compare/v1.10.8...v1.11.0) (2025-08-09)


### Features

* enhance model handling and token limits for ClaudeAI and OpenAI ([6cd1548](https://github.com/diillson/chatcli/commit/6cd1548b330b587a3055677c47893d062956211b))
* enhance model handling and token limits for ClaudeAI and OpenAI ([62b5cce](https://github.com/diillson/chatcli/commit/62b5ccea671f82e78961fa6727dea5489c715ad5))

## [1.10.8](https://github.com/diillson/chatcli/compare/v1.10.7...v1.10.8) (2025-06-20)


### Bug Fixes

* melhorando o gestor de versão. ([15379aa](https://github.com/diillson/chatcli/commit/15379aa9cde99f52396c76173b73607277978263))

## [1.10.7](https://github.com/diillson/chatcli/compare/v1.10.6...v1.10.7) (2025-06-20)


### Bug Fixes

* Rollback mensagem de systema para comandos api OPENAI ASSISTANT. ([f514a47](https://github.com/diillson/chatcli/commit/f514a47a96d15426f4fc8467964a27cea9b3d836))

## [1.10.6](https://github.com/diillson/chatcli/compare/v1.10.5...v1.10.6) (2025-06-20)


### Bug Fixes

* teste fluxo de sync automático na develop. ([9fffa16](https://github.com/diillson/chatcli/commit/9fffa16523e455dcd6c541a1fd90227f18593c94))
* teste fluxo de sync automático na develop. ([80ac765](https://github.com/diillson/chatcli/commit/80ac765b8ee4571310e775d945a407c9861b2485))

## [1.10.5](https://github.com/diillson/chatcli/compare/v1.10.4...v1.10.5) (2025-06-20)


### Bug Fixes

* teste fluxo de sync automático na develop. ([ade51ec](https://github.com/diillson/chatcli/commit/ade51ecfdb01e6351ca5f3d001fb3af315092b29))
* teste fluxo de sync automático na develop. ([579c6f6](https://github.com/diillson/chatcli/commit/579c6f658b49c613661140ffab7cc7adb90bd02f))

## [1.10.4](https://github.com/diillson/chatcli/compare/v1.10.3...v1.10.4) (2025-06-20)


### Bug Fixes

* corrigindo erro no lint, nova linha redundante. ([d314362](https://github.com/diillson/chatcli/commit/d314362c9e63b358320d20d6e1416786c5d12870))
* nova mensagem de prompt para Assistante API OPENAI. ([19232d6](https://github.com/diillson/chatcli/commit/19232d6e9bc4da33ce86358911057bb71dfb39c7))

## [1.10.3](https://github.com/diillson/chatcli/compare/v1.10.2...v1.10.3) (2025-06-19)


### Bug Fixes

* Update message version. ([5f49fec](https://github.com/diillson/chatcli/commit/5f49fecf519837fb3602fce374d30f4ccbc5134f))
* Update message version. ([c69df28](https://github.com/diillson/chatcli/commit/c69df286153e87b0861a419a05e61d15cd890327))

## [1.10.2](https://github.com/diillson/chatcli/compare/v1.10.1...v1.10.2) (2025-06-19)


### Bug Fixes

* corrigir referências e fluxo do workflow de release ([ca9150e](https://github.com/diillson/chatcli/commit/ca9150e0bcc58ae6c7bfdc1883a7b48099641b82))
* corrigir referências e fluxo do workflow de release ([334d298](https://github.com/diillson/chatcli/commit/334d2988af29b569f0f50372da67339ab543d7c6))

## [1.10.1](https://github.com/diillson/chatcli/compare/v1.10.0...v1.10.1) (2025-06-19)


### Bug Fixes

* Melhorar comparação semântica de versões e gestão de recursos ([eed3722](https://github.com/diillson/chatcli/commit/eed3722fbbc218bfc4c5bbd2c5dc5d3d3f25dab0))

## [1.10.0](https://github.com/diillson/chatcli/compare/v1.9.0...v1.10.0) (2025-06-18)


### Features

* **lint:** depois de lint resolvido, removendo comentarios de debug ([b310bc1](https://github.com/diillson/chatcli/commit/b310bc1e25d53cf87cca7f6b9b5e84cf9846a8a8))
* **lint:** resolvendo lint sinalizado ([1e55571](https://github.com/diillson/chatcli/commit/1e55571a00afeeae31b6de24d30a16482dbe20f1))
* **version:** adiciona comando para verificar e exibir informações de versão ([5870eac](https://github.com/diillson/chatcli/commit/5870eac23dec47ea8fbed5de0ebbf5f20b6ebc98))
