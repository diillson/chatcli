# Changelog

## [1.43.4](https://github.com/diillson/chatcli/compare/v1.43.3...v1.43.4) (2025-12-23)


### Bug Fixes

* **cli:** add HTML decoding and improved multiline command handling ([#424](https://github.com/diillson/chatcli/issues/424)) ([#425](https://github.com/diillson/chatcli/issues/425)) ([4951dbe](https://github.com/diillson/chatcli/commit/4951dbe4e75f29a3702324529affe1f4e7512d96))

## [1.43.3](https://github.com/diillson/chatcli/compare/v1.43.2...v1.43.3) (2025-12-20)


### Bug Fixes

* **cli:** clarify `--encoding base64` requirement in coder mode help text ([#421](https://github.com/diillson/chatcli/issues/421)) ([#422](https://github.com/diillson/chatcli/issues/422)) ([d45330c](https://github.com/diillson/chatcli/commit/d45330c7360c56d32f85162fef21ffc7a6c74c37))

## [1.43.2](https://github.com/diillson/chatcli/compare/v1.43.1...v1.43.2) (2025-12-20)


### Bug Fixes

* **cli:** improve handling of trailing backslashes in tool arguments ([#418](https://github.com/diillson/chatcli/issues/418)) ([488df78](https://github.com/diillson/chatcli/commit/488df78cee75b4f2167b8eb5110594bfd5aa6037))

## [1.43.1](https://github.com/diillson/chatcli/compare/v1.43.0...v1.43.1) (2025-12-19)


### Bug Fixes

* **cli:** add multiline tool args normalization and stricter validation in coder mode ([#414](https://github.com/diillson/chatcli/issues/414)) ([#415](https://github.com/diillson/chatcli/issues/415)) ([acc0717](https://github.com/diillson/chatcli/commit/acc0717f5a31e8a734b2a6cc5ff7fdb220b0f0b6))

## [1.43.0](https://github.com/diillson/chatcli/compare/v1.42.0...v1.43.0) (2025-12-19)


### Features

* **cli:** enhance coder mode with role-based context, stricter tool call validation, and execution profile management ([#411](https://github.com/diillson/chatcli/issues/411)) ([#412](https://github.com/diillson/chatcli/issues/412)) ([32de3b6](https://github.com/diillson/chatcli/commit/32de3b6e2b2134e8045d90e0024b43ecfe597ed4))

## [1.42.0](https://github.com/diillson/chatcli/compare/v1.41.1...v1.42.0) (2025-12-19)


### Features

* **cli:** add one-shot execution for coder mode and improve translations ([#407](https://github.com/diillson/chatcli/issues/407)) ([f335db7](https://github.com/diillson/chatcli/commit/f335db764f7d3ed3181abd658e1730016cf75aa7))

## [1.41.1](https://github.com/diillson/chatcli/compare/v1.41.0...v1.41.1) (2025-12-18)


### Bug Fixes

* **cli:** enforce reasoning step before tool execution in coder mode and improve error rendering ([#403](https://github.com/diillson/chatcli/issues/403)) ([#404](https://github.com/diillson/chatcli/issues/404)) ([7c90c6e](https://github.com/diillson/chatcli/commit/7c90c6e49aeabfee50d2f6fff333667293e1ba2a))

## [1.41.0](https://github.com/diillson/chatcli/compare/v1.40.0...v1.41.0) (2025-12-17)


### Features

* **plugins-examples:** enhance `chatcli-coder` with `exec` command improvements ([#400](https://github.com/diillson/chatcli/issues/400)) ([#401](https://github.com/diillson/chatcli/issues/401)) ([2f5100a](https://github.com/diillson/chatcli/commit/2f5100a04cc8fc756721894883b3becf2b678822))

## [1.40.0](https://github.com/diillson/chatcli/compare/v1.39.3...v1.40.0) (2025-12-10)


### Features

* **cli:** introduce coder mode and enhance agent UI ([#397](https://github.com/diillson/chatcli/issues/397)) ([#398](https://github.com/diillson/chatcli/issues/398)) ([19706c1](https://github.com/diillson/chatcli/commit/19706c1fdda11721f326e0bc749a1abfb0f90c0d))

## [1.39.3](https://github.com/diillson/chatcli/compare/v1.39.2...v1.39.3) (2025-12-04)


### Bug Fixes

* **chatcli-eks:** enhance Istio configuration and update plugin version ([#394](https://github.com/diillson/chatcli/issues/394)) ([#395](https://github.com/diillson/chatcli/issues/395)) ([54b4f23](https://github.com/diillson/chatcli/commit/54b4f231f49f8d8cbc6adbbcc35c82ba215dc68f))

## [1.39.2](https://github.com/diillson/chatcli/compare/v1.39.1...v1.39.2) (2025-11-29)


### Bug Fixes

* **chatcli-eks:** remove DynamoDB lock implementation and add Pulumi Cloud token support for non-interactive sessions ([#391](https://github.com/diillson/chatcli/issues/391)) ([#392](https://github.com/diillson/chatcli/issues/392)) ([cdc5c29](https://github.com/diillson/chatcli/commit/cdc5c29b00dc57822264c3971987063e5b234f84))

## [1.39.1](https://github.com/diillson/chatcli/compare/v1.39.0...v1.39.1) (2025-11-28)


### Bug Fixes

* docs(chatcli-eks): add Pulumi CLI installation instructions to README ([#388](https://github.com/diillson/chatcli/issues/388)) ([#389](https://github.com/diillson/chatcli/issues/389)) ([e960e5b](https://github.com/diillson/chatcli/commit/e960e5b531401db4336068a0353f534d9c79982b))

## [1.39.0](https://github.com/diillson/chatcli/compare/v1.38.14...v1.39.0) (2025-11-28)


### Features

* **plugins-examples:** add `chatcli-eks` plugin for EKS cluster deployment ([#385](https://github.com/diillson/chatcli/issues/385)) ([#386](https://github.com/diillson/chatcli/issues/386)) ([5fec508](https://github.com/diillson/chatcli/commit/5fec5083d7b07a4be3dd9a7756f5b6106b78271a))

## [1.38.14](https://github.com/diillson/chatcli/compare/v1.38.13...v1.38.14) (2025-11-22)


### Bug Fixes

* **plugins-examples:** enhance `chatcli-docs-flatten` with schema generation, better glob matching, and improved CLI ([#382](https://github.com/diillson/chatcli/issues/382)) ([#383](https://github.com/diillson/chatcli/issues/383)) ([081b716](https://github.com/diillson/chatcli/commit/081b71624c97296f1a9acaccc79ad8e82dca69bc))

## [1.38.13](https://github.com/diillson/chatcli/compare/v1.38.12...v1.38.13) (2025-11-21)


### Bug Fixes

* **plugins-examples:** extend chatcli-docs-flatten with Git integration and additional output formats ([#379](https://github.com/diillson/chatcli/issues/379)) ([#380](https://github.com/diillson/chatcli/issues/380)) ([de6d5db](https://github.com/diillson/chatcli/commit/de6d5db1285ccb0d6a3b29f97f2fa3f0cfd3da18))

## [1.38.12](https://github.com/diillson/chatcli/compare/v1.38.11...v1.38.12) (2025-11-21)


### Bug Fixes

* **cli:** ensure terminal state is reset after plugin installation confirmation ([#376](https://github.com/diillson/chatcli/issues/376)) ([#377](https://github.com/diillson/chatcli/issues/377)) ([097fdca](https://github.com/diillson/chatcli/commit/097fdca55770f1ff2cae6d0eb886423961fb6ff1))

## [1.38.11](https://github.com/diillson/chatcli/compare/v1.38.10...v1.38.11) (2025-11-21)


### Bug Fixes

* **plugins-examples:** add `chatcli-docs-flatten` plugin for Markdown processing and chunk generation ([#373](https://github.com/diillson/chatcli/issues/373)) ([#374](https://github.com/diillson/chatcli/issues/374)) ([0e0a780](https://github.com/diillson/chatcli/commit/0e0a780251b454ce711824301748317ff0f8d286))

## [1.38.10](https://github.com/diillson/chatcli/compare/v1.38.9...v1.38.10) (2025-11-20)


### Bug Fixes

* **plugins-examples:** enhance `chatcli-kind` with improved registry handling and custom Nginx cert-gen options ([#370](https://github.com/diillson/chatcli/issues/370)) ([#371](https://github.com/diillson/chatcli/issues/371)) ([db4a43e](https://github.com/diillson/chatcli/commit/db4a43e27a99aa27828e6989d340740d7ecb8afd))

## [1.38.9](https://github.com/diillson/chatcli/compare/v1.38.8...v1.38.9) (2025-11-19)


### Bug Fixes

* **plugins-examples:** enhance chatcli-kind with private registry support and Istio improvements ([#367](https://github.com/diillson/chatcli/issues/367)) ([#368](https://github.com/diillson/chatcli/issues/368)) ([c4d36e3](https://github.com/diillson/chatcli/commit/c4d36e3d2f686da0e5b375226c834894845099a9))

## [1.38.8](https://github.com/diillson/chatcli/compare/v1.38.7...v1.38.8) (2025-11-17)


### Bug Fixes

* **plugins-examples:** introduce extensive Pulumi-based `chatcli-k8s-cloud-refactor` example ([#365](https://github.com/diillson/chatcli/issues/365)) ([d182a45](https://github.com/diillson/chatcli/commit/d182a4529deaaff1ea508879d09c63168470bbfd))

## [1.38.7](https://github.com/diillson/chatcli/compare/v1.38.6...v1.38.7) (2025-11-17)


### Bug Fixes

* **docs-plugins:** improve formatting and provide code block examples in `agentic-plugins.md` ([#361](https://github.com/diillson/chatcli/issues/361)) ([#362](https://github.com/diillson/chatcli/issues/362)) ([a3f599f](https://github.com/diillson/chatcli/commit/a3f599fca10e66e908d8ec87ce80dad5da4bc7cc))

## [1.38.6](https://github.com/diillson/chatcli/compare/v1.38.5...v1.38.6) (2025-11-17)


### Bug Fixes

* **plugins:** reorganize and extend `chatcli-k8s-cloud` and `chatcli-kind` examples ([#358](https://github.com/diillson/chatcli/issues/358)) ([#359](https://github.com/diillson/chatcli/issues/359)) ([6d6a866](https://github.com/diillson/chatcli/commit/6d6a866f6aae5ffd74e16aa5d4df5ec3310bf51b))

## [1.38.5](https://github.com/diillson/chatcli/compare/v1.38.4...v1.38.5) (2025-11-12)


### Bug Fixes

* **cli:** improve context subcommand autocompletions and flag handling ([#355](https://github.com/diillson/chatcli/issues/355)) ([#356](https://github.com/diillson/chatcli/issues/356)) ([14b7f36](https://github.com/diillson/chatcli/commit/14b7f36c65d60abfee5c8684dcd86196574df9c0))

## [1.38.4](https://github.com/diillson/chatcli/compare/v1.38.3...v1.38.4) (2025-11-11)


### Bug Fixes

* **plugins:** introduce `[@docker-clean](https://github.com/docker-clean)` plugin example for managing Docker assets ([#352](https://github.com/diillson/chatcli/issues/352)) ([#353](https://github.com/diillson/chatcli/issues/353)) ([53b5065](https://github.com/diillson/chatcli/commit/53b50653e503f49e8067e41afd855a9ca16f6dc6))

## [1.38.3](https://github.com/diillson/chatcli/compare/v1.38.2...v1.38.3) (2025-11-11)


### Bug Fixes

* **plugins:** enhance `[@kind](https://github.com/kind)` plugin with new features and improved logging ([#349](https://github.com/diillson/chatcli/issues/349)) ([#350](https://github.com/diillson/chatcli/issues/350)) ([ba2a97f](https://github.com/diillson/chatcli/commit/ba2a97fdeb01a40ea470a3011a5bc468ad2c58ba))

## [1.38.2](https://github.com/diillson/chatcli/compare/v1.38.1...v1.38.2) (2025-11-10)


### Bug Fixes

* **plugins:** add new `[@kind](https://github.com/kind)` plugin example for Kubernetes cluster management ([#346](https://github.com/diillson/chatcli/issues/346)) ([#347](https://github.com/diillson/chatcli/issues/347)) ([14ffcd8](https://github.com/diillson/chatcli/commit/14ffcd893ff1c705e3bb00262a1749db7e418194))

## [1.38.1](https://github.com/diillson/chatcli/compare/v1.38.0...v1.38.1) (2025-11-09)


### Bug Fixes

* revise command reference and enhance plugin examples ([#343](https://github.com/diillson/chatcli/issues/343)) ([#344](https://github.com/diillson/chatcli/issues/344)) ([34f6175](https://github.com/diillson/chatcli/commit/34f617561c2ad315b359b98d9f9ff4aa79629221))

## [1.38.0](https://github.com/diillson/chatcli/compare/v1.37.0...v1.38.0) (2025-11-09)


### Features

* Implement Agentic AI Engine and Extensible Plugin System  ([#340](https://github.com/diillson/chatcli/issues/340)) ([#341](https://github.com/diillson/chatcli/issues/341)) ([cc6cbde](https://github.com/diillson/chatcli/commit/cc6cbde8584ddf2f15f671d28b24d97eef4e4029))

## [1.37.0](https://github.com/diillson/chatcli/compare/v1.36.2...v1.37.0) (2025-11-05)


### Features

* **docs:** expand ChatCLI documentation with recipes, cookbook index, and advanced workflows ([#337](https://github.com/diillson/chatcli/issues/337)) ([#338](https://github.com/diillson/chatcli/issues/338)) ([97ff013](https://github.com/diillson/chatcli/commit/97ff013b9b4e27bc53453eb9e4ff5da0245b0bd5))

## [1.36.2](https://github.com/diillson/chatcli/compare/v1.36.1...v1.36.2) (2025-11-05)


### Bug Fixes

* update hero image path and adjust CTA button URL in index.md ([#334](https://github.com/diillson/chatcli/issues/334)) ([#335](https://github.com/diillson/chatcli/issues/335)) ([732c746](https://github.com/diillson/chatcli/commit/732c746f5ce282145182e785e48da1e0ec4fbf04))

## [1.36.1](https://github.com/diillson/chatcli/compare/v1.36.0...v1.36.1) (2025-11-05)


### Bug Fixes

* use `npm ci` for consistent installs and add package.json for ghpages dependencies ([#331](https://github.com/diillson/chatcli/issues/331)) ([#332](https://github.com/diillson/chatcli/issues/332)) ([745d3ac](https://github.com/diillson/chatcli/commit/745d3acd9b2a5e36a34fb16505c6193c074ac19c))

## [1.36.0](https://github.com/diillson/chatcli/compare/v1.35.0...v1.36.0) (2025-11-05)


### Features

* update create document gihubpages. ([#328](https://github.com/diillson/chatcli/issues/328)) ([#329](https://github.com/diillson/chatcli/issues/329)) ([a1a5807](https://github.com/diillson/chatcli/commit/a1a5807e5a6ce09f79d4293b5f1d14509ad1354d))

## [1.35.0](https://github.com/diillson/chatcli/compare/v1.34.0...v1.35.0) (2025-10-30)


### Features

* **cli:** add support for updating contexts and enhance context creation ([#325](https://github.com/diillson/chatcli/issues/325)) ([#326](https://github.com/diillson/chatcli/issues/326)) ([39e6100](https://github.com/diillson/chatcli/commit/39e610019a7bde471820a6b8e800abd088ff7028))

## [1.34.0](https://github.com/diillson/chatcli/compare/v1.33.0...v1.34.0) (2025-10-29)


### Features

* **cli:** add deep inspection support for contexts and enhance CLI suggestions ([#322](https://github.com/diillson/chatcli/issues/322)) ([#323](https://github.com/diillson/chatcli/issues/323)) ([8c522c9](https://github.com/diillson/chatcli/commit/8c522c9949f155b30677dafc92fc3cf2754b1e22))

## [1.33.0](https://github.com/diillson/chatcli/compare/v1.32.0...v1.33.0) (2025-10-29)


### Features

* **cli:** implement context management features and enhance dependencies ([#319](https://github.com/diillson/chatcli/issues/319)) ([#320](https://github.com/diillson/chatcli/issues/320)) ([f4705f3](https://github.com/diillson/chatcli/commit/f4705f318168eb87cc9b95c9c4bedb8c9c5fc413))

## [1.32.0](https://github.com/diillson/chatcli/compare/v1.31.2...v1.32.0) (2025-10-28)


### Features

* **cli:** add support for custom ignore patterns using .chatignore ([#316](https://github.com/diillson/chatcli/issues/316)) ([#317](https://github.com/diillson/chatcli/issues/317)) ([ff65d9e](https://github.com/diillson/chatcli/commit/ff65d9efbae92f75f740d25d524685f077a2907e))

## [1.31.2](https://github.com/diillson/chatcli/compare/v1.31.1...v1.31.2) (2025-10-21)


### Bug Fixes

* **assets:** add demo GIF for chat CLI ([#313](https://github.com/diillson/chatcli/issues/313)) ([#314](https://github.com/diillson/chatcli/issues/314)) ([8463955](https://github.com/diillson/chatcli/commit/8463955a2c38733bd8ee2b29bd18c80418cc0eb4))

## [1.31.1](https://github.com/diillson/chatcli/compare/v1.31.0...v1.31.1) (2025-10-20)


### Bug Fixes

* **cli:** add support for `--agent-id` flag and update docs/i18n for StackSpot integration ([#310](https://github.com/diillson/chatcli/issues/310)) ([#311](https://github.com/diillson/chatcli/issues/311)) ([d10c2a9](https://github.com/diillson/chatcli/commit/d10c2a9d9e885e42d72ad9f093f01033781f2514))

## [1.31.0](https://github.com/diillson/chatcli/compare/v1.30.0...v1.31.0) (2025-10-19)


### Features

* **i18n:** implement internationalization support and update error messages. ([#307](https://github.com/diillson/chatcli/issues/307)) ([#308](https://github.com/diillson/chatcli/issues/308)) ([386e213](https://github.com/diillson/chatcli/commit/386e2132d2c6777d6733d7e174d2f113b9351205))

## [1.30.0](https://github.com/diillson/chatcli/compare/v1.29.1...v1.30.0) (2025-10-14)


### Features

* **llm:** refactor realm handling and update StackSpot client configuration ([#304](https://github.com/diillson/chatcli/issues/304)) ([#305](https://github.com/diillson/chatcli/issues/305)) ([a1e490a](https://github.com/diillson/chatcli/commit/a1e490ab8e07102338f23062e5fb97f1bc9d70e5))

## [1.29.1](https://github.com/diillson/chatcli/compare/v1.29.0...v1.29.1) (2025-10-08)


### Bug Fixes

* **config:** remove init-based global config initialization ([#301](https://github.com/diillson/chatcli/issues/301)) ([#302](https://github.com/diillson/chatcli/issues/302)) ([d547ba6](https://github.com/diillson/chatcli/commit/d547ba64792b6c7b1e95cb111ca8889b922bf588))

## [1.29.0](https://github.com/diillson/chatcli/compare/v1.28.5...v1.29.0) (2025-10-08)


### Features

* **agent:** add comprehensive command validation and execution framework ([#298](https://github.com/diillson/chatcli/issues/298)) ([#299](https://github.com/diillson/chatcli/issues/299)) ([7c83e03](https://github.com/diillson/chatcli/commit/7c83e03c7c4b72750d3e48013fcf458262283d81))

## [1.28.5](https://github.com/diillson/chatcli/compare/v1.28.4...v1.28.5) (2025-10-01)


### Bug Fixes

* **catalog:** add support for Claude Sonnet 4.5 with extended context window and aliases ([#295](https://github.com/diillson/chatcli/issues/295)) ([#296](https://github.com/diillson/chatcli/issues/296)) ([a78fda5](https://github.com/diillson/chatcli/commit/a78fda51d03e864687eec33f5e721eee9b2481e6))

## [1.28.4](https://github.com/diillson/chatcli/compare/v1.28.3...v1.28.4) (2025-09-29)


### Bug Fixes

* **workflows:** streamline CI, PR, and release automation ([#293](https://github.com/diillson/chatcli/issues/293)) ([1003413](https://github.com/diillson/chatcli/commit/1003413945fb9d3928678a8cdd405f7bb7b1c25b))

## [1.28.3](https://github.com/diillson/chatcli/compare/v1.28.2...v1.28.3) (2025-09-28)


### Bug Fixes

* **agent:** prevent screen clearing issue in certain terminal providers by refining redraw logic ([#287](https://github.com/diillson/chatcli/issues/287)) ([#288](https://github.com/diillson/chatcli/issues/288)) ([dc575e5](https://github.com/diillson/chatcli/commit/dc575e5a0cb64eccff9ee65af1c08429a32145ca))

## [1.28.2](https://github.com/diillson/chatcli/compare/v1.28.1...v1.28.2) (2025-09-28)


### Bug Fixes

* **env:** temporarily disable environment variable check and update workflow secrets ([#284](https://github.com/diillson/chatcli/issues/284)) ([#285](https://github.com/diillson/chatcli/issues/285)) ([7303557](https://github.com/diillson/chatcli/commit/730355760baaf21312f31bbb8845f2f49163abe3))

## [1.28.1](https://github.com/diillson/chatcli/compare/v1.28.0...v1.28.1) (2025-09-27)


### Bug Fixes

* **ollama:** add "thinking" filter with customizable option ([#281](https://github.com/diillson/chatcli/issues/281)) ([#282](https://github.com/diillson/chatcli/issues/282)) ([b1a649b](https://github.com/diillson/chatcli/commit/b1a649bf4d8ad890b9e9255bb4b49190d2f12774))

## [1.28.0](https://github.com/diillson/chatcli/compare/v1.27.2...v1.28.0) (2025-09-22)


### Features

* **agent:** implement enhanced UI and commands for Agent Mode ([#278](https://github.com/diillson/chatcli/issues/278)) ([#279](https://github.com/diillson/chatcli/issues/279)) ([8c742ae](https://github.com/diillson/chatcli/commit/8c742ae628163cc64a993040cc256ccfc19b2641))

## [1.27.2](https://github.com/diillson/chatcli/compare/v1.27.1...v1.27.2) (2025-09-22)


### Bug Fixes

* **agent:** enhance security and customization for Agent Mode ([#276](https://github.com/diillson/chatcli/issues/276)) ([e546cae](https://github.com/diillson/chatcli/commit/e546cae274e392c4981256d06d6b7fcd53c817c9))

## [1.27.1](https://github.com/diillson/chatcli/compare/v1.27.0...v1.27.1) (2025-09-21)


### Bug Fixes

* **llm:** introduce retry logic and customizable backoff for API clients ([#270](https://github.com/diillson/chatcli/issues/270)) ([#271](https://github.com/diillson/chatcli/issues/271)) ([0c1fd67](https://github.com/diillson/chatcli/commit/0c1fd671bb5fc45a08c5c8d592231037713d2b1b))

## [1.27.0](https://github.com/diillson/chatcli/compare/v1.26.0...v1.27.0) (2025-09-20)


### Features

* **cli,llm:** implement override for max_tokens across clients and CLI ([#267](https://github.com/diillson/chatcli/issues/267)) ([#268](https://github.com/diillson/chatcli/issues/268)) ([9236384](https://github.com/diillson/chatcli/commit/9236384cf975bdfd1f3c729312a5e243df90c7fd))

## [1.26.0](https://github.com/diillson/chatcli/compare/v1.25.2...v1.26.0) (2025-09-18)


### Features

* **llm:** add Ollama provider with support for local models ([#264](https://github.com/diillson/chatcli/issues/264)) ([#265](https://github.com/diillson/chatcli/issues/265)) ([df4a847](https://github.com/diillson/chatcli/commit/df4a8475da5b8d22bf3597f8df7999937e277386))

## [1.25.2](https://github.com/diillson/chatcli/compare/v1.25.1...v1.25.2) (2025-09-15)


### Bug Fixes

* **cli:** add command flag suggestions and improve ENV log configuration ([#261](https://github.com/diillson/chatcli/issues/261)) ([#262](https://github.com/diillson/chatcli/issues/262)) ([d85f46f](https://github.com/diillson/chatcli/commit/d85f46fbffb43a80ed49cd9a169b4c08ed29a1a0))

## [1.25.1](https://github.com/diillson/chatcli/compare/v1.25.0...v1.25.1) (2025-09-15)


### Bug Fixes

* **version:** correct version output and align CLI/main integration ([#258](https://github.com/diillson/chatcli/issues/258)) ([#259](https://github.com/diillson/chatcli/issues/259)) ([06357d8](https://github.com/diillson/chatcli/commit/06357d8d5f36b4e9d19ce985929666cf8cd1d4fd))

## [1.25.0](https://github.com/diillson/chatcli/compare/v1.24.0...v1.25.0) (2025-09-15)


### Features

* **cli,llm:** dynamic max_tokens handling and enhanced command guide ([#256](https://github.com/diillson/chatcli/issues/256)) ([2dfe5ae](https://github.com/diillson/chatcli/commit/2dfe5aeb5573c80d587031fbaa3e5d4896faa7d2))

## [1.24.0](https://github.com/diillson/chatcli/compare/v1.23.0...v1.24.0) (2025-09-14)


### Features

* **llm:** integrate xAI provider with support for Grok models ([#248](https://github.com/diillson/chatcli/issues/248)) ([#249](https://github.com/diillson/chatcli/issues/249)) ([28b2269](https://github.com/diillson/chatcli/commit/28b2269051ecdd8a4dc09c1adf18afeb3aa42ca8))

## [1.23.0](https://github.com/diillson/chatcli/compare/v1.22.5...v1.23.0) (2025-09-13)


### Features

* **cli:** adiciona gerenciamento de sessões e geração de PR assistida por IA ([#246](https://github.com/diillson/chatcli/issues/246)) ([766e484](https://github.com/diillson/chatcli/commit/766e48421d3dce8ee9ba5e6b4a3c53f24a15440b))

## [1.22.5](https://github.com/diillson/chatcli/compare/v1.22.4...v1.22.5) (2025-09-11)


### Bug Fixes

* **cli:** Ensure command history is saved on graceful exit ([#237](https://github.com/diillson/chatcli/issues/237)) ([#238](https://github.com/diillson/chatcli/issues/238)) ([5931166](https://github.com/diillson/chatcli/commit/5931166369c4f45990abf659c92585777c1b69b2))

## [1.22.4](https://github.com/diillson/chatcli/compare/v1.22.3...v1.22.4) (2025-09-11)


### Bug Fixes

* **cli:** simplify dynamic header and footer formatting in AgentMode ([#234](https://github.com/diillson/chatcli/issues/234)) ([#235](https://github.com/diillson/chatcli/issues/235)) ([e5dea76](https://github.com/diillson/chatcli/commit/e5dea76d5ccbb35ef14b1e731beecd2ef0b397d9))

## [1.22.3](https://github.com/diillson/chatcli/compare/v1.22.2...v1.22.3) (2025-09-11)


### Bug Fixes

* **cli:** add build constraints for platform-specific signal files ([#231](https://github.com/diillson/chatcli/issues/231)) ([#232](https://github.com/diillson/chatcli/issues/232)) ([db6e1aa](https://github.com/diillson/chatcli/commit/db6e1aa707a7f518af8b700573858f250a3c6d82))

## [1.22.2](https://github.com/diillson/chatcli/compare/v1.22.1...v1.22.2) (2025-09-10)


### Bug Fixes

* **cli:** split `forceRefreshPrompt` by platform to improve compatibility ([#228](https://github.com/diillson/chatcli/issues/228)) ([#229](https://github.com/diillson/chatcli/issues/229)) ([74dd80f](https://github.com/diillson/chatcli/commit/74dd80f43e145bdece3b96487182a4a978df6529))

## [1.22.1](https://github.com/diillson/chatcli/compare/v1.22.0...v1.22.1) (2025-09-10)


### Bug Fixes

* **dependencies:** remove unused modules and migrate `syscall` to `x/sys/unix` in `cli/cli.go` ([#225](https://github.com/diillson/chatcli/issues/225)) ([#226](https://github.com/diillson/chatcli/issues/226)) ([de9f6c5](https://github.com/diillson/chatcli/commit/de9f6c5b6832c0331ad65f0c9703bd921b97576a))

## [1.22.0](https://github.com/diillson/chatcli/compare/v1.21.0...v1.22.0) (2025-09-10)


### Features

* **cli:** Enhance AgentMode UX and refactor input handling ([9e527ce](https://github.com/diillson/chatcli/commit/9e527ceb37a6ed23884aa7f3bba87668360eb870))

## [1.21.0](https://github.com/diillson/chatcli/compare/v1.20.1...v1.21.0) (2025-09-07)


### Features

* **multiline-remove:** rollback to v1.19.1 ([#217](https://github.com/diillson/chatcli/issues/217)) ([#218](https://github.com/diillson/chatcli/issues/218)) ([d1e4650](https://github.com/diillson/chatcli/commit/d1e465017506f55a50b0fc1341023a4204dc34ad))

## [1.19.1](https://github.com/diillson/chatcli/compare/v1.19.0...v1.19.1) (2025-09-06)


### Bug Fixes

* **agent:** Improve command parsing and resolve failing utility tests  ([#208](https://github.com/diillson/chatcli/issues/208)) ([#209](https://github.com/diillson/chatcli/issues/209)) ([ebc4a4c](https://github.com/diillson/chatcli/commit/ebc4a4cfe694b301643fe2e86d0a84b02ff88e21))

## [1.19.0](https://github.com/diillson/chatcli/compare/v1.18.1...v1.19.0) (2025-09-04)


### Features

* **cli:** add agent one-shot mode with auto-execution support ([#205](https://github.com/diillson/chatcli/issues/205)) ([#206](https://github.com/diillson/chatcli/issues/206)) ([9e90fbf](https://github.com/diillson/chatcli/commit/9e90fbfa0e916c05eed8df72a0467509605ed06b))

## [1.18.1](https://github.com/diillson/chatcli/compare/v1.18.0...v1.18.1) (2025-09-03)


### Bug Fixes

* (test_stackspot_client): add and enhance unit tests, refactor for configurability ([#203](https://github.com/diillson/chatcli/issues/203)) ([6f10eee](https://github.com/diillson/chatcli/commit/6f10eeedef497ad441039c9f33983f134cf78a7d))

## [1.18.0](https://github.com/diillson/chatcli/compare/v1.17.4...v1.18.0) (2025-09-03)


### Features

* **welcome/UnitTests:** improve test robustness by adding error check for form parsing ([#200](https://github.com/diillson/chatcli/issues/200)) ([43ec5d5](https://github.com/diillson/chatcli/commit/43ec5d5ab007aed659640321500933590a4e47d5))

## [1.17.4](https://github.com/diillson/chatcli/compare/v1.17.3...v1.17.4) (2025-08-26)


### Bug Fixes

* **license:** add license headers to all source files ([#192](https://github.com/diillson/chatcli/issues/192)) ([#193](https://github.com/diillson/chatcli/issues/193)) ([2d8242b](https://github.com/diillson/chatcli/commit/2d8242ba58c263ebcb98cef84b6f801428190a09))

## [1.17.3](https://github.com/diillson/chatcli/compare/v1.17.2...v1.17.3) (2025-08-26)


### Bug Fixes

* **ci:** comment out pull_request triggers in CI workflow ([ef9b160](https://github.com/diillson/chatcli/commit/ef9b160fdf94dc33d00974ef93afbb421435e75e))
* **ci:** comment out pull_request triggers in CI workflow ([70e4020](https://github.com/diillson/chatcli/commit/70e402089dfced492174a596dcaa5d2975c043ee))

## [1.17.2](https://github.com/diillson/chatcli/compare/v1.17.1...v1.17.2) (2025-08-26)


### Bug Fixes

* **ci:** comment out pull_request triggers in CI workflow ([c078896](https://github.com/diillson/chatcli/commit/c078896ef121d1b422ec328cfc85b3e90fe20d75))
* **ci:** comment out pull_request triggers in CI workflow ([19d7fb5](https://github.com/diillson/chatcli/commit/19d7fb58c5eb62c9f8f55f1be7ef92231239dc0d))
* **ci:** comment out pull_request triggers in CI workflow ([5658413](https://github.com/diillson/chatcli/commit/5658413ff2e6ca7a5ce98b283db52d8d5a387ff1))
* **ci:** comment out pull_request triggers in CI workflow ([bc43e5c](https://github.com/diillson/chatcli/commit/bc43e5cd54058baa258baf01dbffd7c5c17707c7))
* **ci:** comment out pull_request triggers in CI workflow ([8bd1953](https://github.com/diillson/chatcli/commit/8bd19535059f83493bbdf19b73c05f730f564fb6))
* **main:** update version comment for testing flow ([a679972](https://github.com/diillson/chatcli/commit/a679972efe02bd1459f627ce33f41f180e2599ca))
* **main:** update version comment for testing flow ([55b8fe9](https://github.com/diillson/chatcli/commit/55b8fe92ecec1ad4fb70670f9750cdede752cefa))
* **main:** update version comment for testing flow ([3436a77](https://github.com/diillson/chatcli/commit/3436a777bc3649b5cfd6c5be8226b30c1f153ed1))
* **main:** update version comment for testing flow ([e92c39c](https://github.com/diillson/chatcli/commit/e92c39cdbc5ccc978baa17804357edf9f66a803f))
* **workflows:** restructure workflows for improved release management ([726e885](https://github.com/diillson/chatcli/commit/726e885e4aa3afa62bac822bebb6072ff7ab5929))
* **workflows:** restructure workflows for improved release management ([eedb138](https://github.com/diillson/chatcli/commit/eedb138210b8f8ba33e7eb5940326acd1795a5c4))

## [1.17.1](https://github.com/diillson/chatcli/compare/v1.17.0...v1.17.1) (2025-08-24)


### Bug Fixes

* **fileCommand:** add progress tracking and file count during directo… ([0f7581e](https://github.com/diillson/chatcli/commit/0f7581e6c21869864caedf9aa883601c4fa4e703))
* **fileCommand:** add progress tracking and file count during directory scanning ([d04ebc2](https://github.com/diillson/chatcli/commit/d04ebc2a3dd8eed47de458d1aa660503e28f76ec))

## [1.17.0](https://github.com/diillson/chatcli/compare/v1.16.3...v1.17.0) (2025-08-21)


### Features

* **cli:** enhance cancel operation, model switching, and user contex… ([b44d604](https://github.com/diillson/chatcli/commit/b44d604d0dfe77c09c1980a3a1616205b3c5c236))
* **cli:** enhance cancel operation, model switching, and user context handling ([745624e](https://github.com/diillson/chatcli/commit/745624e0fa461b6d63ef5e4eb779ebd2f7fac34f))
* Enhance CLI Control, Agent Mode, and Cancellable Operations ([a6799f5](https://github.com/diillson/chatcli/commit/a6799f5952e2912ed0eaae730f883b54b7bf034c))

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
