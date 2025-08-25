# Changelog

## [2.0.0](https://github.com/diillson/chatcli/compare/v1.17.4...v2.0.0) (2025-08-25)


### ⚠ BREAKING CHANGES

* **agent:** 

### Features

* **/newsession:** Adicionando novo comando para inicio de nova conversa/sessão ([e9c88d2](https://github.com/diillson/chatcli/commit/e9c88d238b76ea69497c7cb11a4f8d65c66e10bd))
* add Google AI (Gemini) provider support and configuration options ([de71481](https://github.com/diillson/chatcli/commit/de7148104530deca8ec4fb26cb083d2b8f40cf51))
* add Google AI (Gemini) provider support and configuration options ([87dbec8](https://github.com/diillson/chatcli/commit/87dbec885058f3d24ad470e2e5e7782f388ea26b))
* add support for OpenAI Responses API and enhance configuration options ([334f96e](https://github.com/diillson/chatcli/commit/334f96edf2bff4a013962c21863e5f12e5a68182))
* adiciona comando [@command](https://github.com/command) para execução de comandos no sistema. ([7874e85](https://github.com/diillson/chatcli/commit/7874e85a43e636aba1994d3044cb5e3b2c7c2e50))
* Adicionando Autocompletar, a fim de melhor experiencia do usuár… ([5f596d5](https://github.com/diillson/chatcli/commit/5f596d58fc3518a0bc96f6e283e1970c1c0e1e1e))
* Adicionando Autocompletar, a fim de melhor experiencia do usuário, e refatoração de codigo para melhor leitura e manutenção.. ([6c05dfa](https://github.com/diillson/chatcli/commit/6c05dfaf38a6e973b13a5dc96b6f276417f30fe0))
* adicionando comentarios para melhor explicar as funcionalidades e retornando o uso de contante para o defaultOpenAimodel. ([08f4f7e](https://github.com/diillson/chatcli/commit/08f4f7effa02dfe9ffcae22f3f66b7db88c0bc60))
* adicionando comentarios para melhor explicar as funcionalidades e retornando o uso de contante para o defaultOpenAimodel. ([2ee29e4](https://github.com/diillson/chatcli/commit/2ee29e434d61adb1b7ce6ac97c31277a6316abe0))
* adicionando comentarios para melhor explicar as funcionalidades… ([3f40427](https://github.com/diillson/chatcli/commit/3f40427696d9adcb0dff2dc92bfd7d79573fac00))
* adicionando condição de reload de variavéis sem a necessidade de restart do chatcli. ([583d075](https://github.com/diillson/chatcli/commit/583d075cf7bb5df049493b8c5ccf999747edaca5))
* adicionando condição para variavel de provider nula ou vazia. ([9cd46be](https://github.com/diillson/chatcli/commit/9cd46be3c3ab3b82e1010b24a4cbda7a6732bc05))
* adicionando condição para variavel de provider nula ou vazia. ([9aaa4c5](https://github.com/diillson/chatcli/commit/9aaa4c52acc6a97c330356a43230564b77e3aabb))
* adicionando condição para variavel de provider nula ou vazia. ([47eebd4](https://github.com/diillson/chatcli/commit/47eebd47f9444369c353c703d29f4082e9d0e6b5))
* adicionando condição para variavel de provider nula ou vazia. ([92fd47a](https://github.com/diillson/chatcli/commit/92fd47a4ac9276509a11c5df064f4af5f2bddd58))
* Adicionando novo provedor de LLM ClaudeAI. ([8629bc8](https://github.com/diillson/chatcli/commit/8629bc866a5f947e0b3d97ac35d9bd6c799944f9))
* Adicionando novo provedor de LLM ClaudeAI. ([d2346e5](https://github.com/diillson/chatcli/commit/d2346e51b24f8d4312df3151962077139cccd3d8))
* adicionando screeshots. ([93a1e9c](https://github.com/diillson/chatcli/commit/93a1e9cb22f39e64d613df293759ad9fde430482))
* adicionando screeshots. ([1be1b59](https://github.com/diillson/chatcli/commit/1be1b59e309862a68662851c7eaae8be54e16c1c))
* Adicionar o comando ao histórico do liner para persistir em .ch… ([d30df17](https://github.com/diillson/chatcli/commit/d30df178bf384d9c773b7b4c150c57852fb0a860))
* Adicionar o comando ao histórico do liner para persistir em .chatcli_history ([b8733f2](https://github.com/diillson/chatcli/commit/b8733f27f492af08baac299c945be9b23cf46e9f))
* **agent:** ciclo Agent moderno e seguro, menus não-travantes, ciclo iterativo cN, segurança e UX ([6b5aab5](https://github.com/diillson/chatcli/commit/6b5aab5184b4758714065b3fe8bbacb24b8ecf1b))
* **agent:** extendendo modo agent para permitir adição de contexto após um ouput. ([8848c33](https://github.com/diillson/chatcli/commit/8848c335dd7de55bdf29c1efe130acf2d72b339f))
* **agent:** melhorando adição de contexto. ([f32e8fb](https://github.com/diillson/chatcli/commit/f32e8fb946e56bc0524914d0a2a4d391d25ec4cb))
* **agent:** melhorando adição de contexto. ([c18ec06](https://github.com/diillson/chatcli/commit/c18ec06d15fe890d1a3276bbe8f86b89fdebcede))
* Ajustando a logica de renovação de token no post e get da stackspotAI. ([eba46a6](https://github.com/diillson/chatcli/commit/eba46a610d6d50b053979e055bdeedb089216163))
* Ajustando carregamento de historico de comando e rotação após 50MB, disponibilizando o tamanho de historico personalizavel com a variavel : HISTORY_MAX_SIZE ([bb1b5f3](https://github.com/diillson/chatcli/commit/bb1b5f3682398d649a1615d8df0de11743c1cf8b))
* Ajustando carregamento de historico de comando e rotação após 50MB, disponibilizando o tamanho de historico personalizavel com a variavel : HISTORY_MAX_SIZE ([3deb760](https://github.com/diillson/chatcli/commit/3deb760581cf26ada1efe395c7a10946d712042f))
* Ajustando tabulação. ([d7d5770](https://github.com/diillson/chatcli/commit/d7d577077532abdf8ef8ff646bf7e2e9ee51a3d6))
* Ajuste de mensagem para Rate limit e funcionamento do [@command](https://github.com/command) . ([bf31600](https://github.com/diillson/chatcli/commit/bf31600fb3dff39f48baf642656356055dde3f36))
* Ajuste de mensagem para Rate limit e funcionamento do [@command](https://github.com/command) . ([f82c596](https://github.com/diillson/chatcli/commit/f82c596b9df85e9cfd175eb16909ca20d40a4b25))
* Ajuste exponencial backoff ([80c32b6](https://github.com/diillson/chatcli/commit/80c32b6d63b3e3d19942b51fd3952b0141a6529d))
* Ajuste exponencial backoff ([3203194](https://github.com/diillson/chatcli/commit/320319435c450b72152bb315a2878a4da69aff98))
* Ajuste log, liberando variavel para personalizar o tamanho do log para rotação. ([5a43541](https://github.com/diillson/chatcli/commit/5a43541cba7d6736e25f24c0d943517e803e597f))
* Ajuste log, liberando variavel para personalizar o tamanho do log para rotação. ([a5ed1d3](https://github.com/diillson/chatcli/commit/a5ed1d361e81351707f1d22e1eead38c9e09d6a0))
* Ajuste no readme.md, para fins de atualizar doc de operação da CLI. ([76723ee](https://github.com/diillson/chatcli/commit/76723ee9d4a5150aa57f7592738e76442a3b86e7))
* alterando a forma de resgatar a saida de command para associar ao contexto da LLM. ([37c64e1](https://github.com/diillson/chatcli/commit/37c64e1818f48b2c75344d57353bfce484a023a0))
* aplicando boas praticas e SRP. ([b2cdb21](https://github.com/diillson/chatcli/commit/b2cdb2138eb073d2442096c6968a95683ed5b9bb))
* aplicando boas praticas, SRP e afins... ([9b5c96e](https://github.com/diillson/chatcli/commit/9b5c96e3c0bf661e6bac6f5d00f411f3fef6185e))
* aplicando boas praticas, SRP e afins... ([d8e1aed](https://github.com/diillson/chatcli/commit/d8e1aed60f14c2dfec0b956b1a90147f58f84041))
* **assistant:** atualizando a versão da api de assistente para v2 da openai e ajustes de seleção do modelo. ([f637301](https://github.com/diillson/chatcli/commit/f637301f43d1a1f136fceef6073728310850eec1))
* **assistant:** atualizando o modo agent para compatibilidade com api de assistente da OpenAI. ([1517b57](https://github.com/diillson/chatcli/commit/1517b5773e2162fabede3970535cf891d4d70d6d))
* **assistent:** atualizando a versão d aapi de assistente para v2 da openai. ([b2da304](https://github.com/diillson/chatcli/commit/b2da304851d1412b81c867379a942fc477357980))
* Atualiza README.md ([9ed07d6](https://github.com/diillson/chatcli/commit/9ed07d625b77c75ac0485adfe7a7776536cda380))
* Atualiza README.md ([af52f39](https://github.com/diillson/chatcli/commit/af52f39dee107a05c8b6faceaa3cfef085f1e245))
* Atualizando .gitignore, ajustando README.md e refatorando cli.go ([57d013c](https://github.com/diillson/chatcli/commit/57d013c94da865435e162eefddada6104877f989))
* Atualizando .gitignore, ajustando README.md e refatorando cli.go ([e11f863](https://github.com/diillson/chatcli/commit/e11f8632407fee4fc1c3e0cae099c34eaf372fb2))
* Atualizando o Readme.md com a nova condição de reload de variavéis sem a necessidade de restart do chatcli. ([37b6175](https://github.com/diillson/chatcli/commit/37b6175af2bb101fef67be5e022a64623f1d736e))
* centraliza valores padrão e padroniza notificações de configura… ([1a55503](https://github.com/diillson/chatcli/commit/1a55503a32edc2ceaeb57c527e23ac7ab72118b4))
* centraliza valores padrão e padroniza notificações de configura… ([1b60d02](https://github.com/diillson/chatcli/commit/1b60d024faea20802285ab1946a0855e83be8b6f))
* centraliza valores padrão e padroniza notificações de configuração. ([392ecc4](https://github.com/diillson/chatcli/commit/392ecc4330a1f0c808e8356b592bbde2cf2bd4e1))
* centraliza valores padrão e padroniza notificações de configuração. ([c6efd89](https://github.com/diillson/chatcli/commit/c6efd898750bda3f75e094c50195fb082df46b06))
* **chunk:** testando nova feature de chunks. ([4e1c587](https://github.com/diillson/chatcli/commit/4e1c5874d32c7a7230a0ed64efabc3088c76b89c))
* **chunk:** testando nova feature de chunks. ([4b7135f](https://github.com/diillson/chatcli/commit/4b7135fd6ce828620db4bf69ab9b2232ff65c711))
* **cli:** add one-shot mode with prompt flags, provider, and model o… ([bc5b108](https://github.com/diillson/chatcli/commit/bc5b10819f873973302748134848a28b6a87fbb4))
* **cli:** add one-shot mode with prompt flags, provider, and model overrides ([8ff908e](https://github.com/diillson/chatcli/commit/8ff908e03557eaa09a414de406901285b6270cdb))
* **cli:** add sensitive data sanitization and enhance configuration … ([66ece6a](https://github.com/diillson/chatcli/commit/66ece6a54e1742116cc1e4456cdfefad021ea5c8))
* **cli:** add sensitive data sanitization and enhance configuration commands ([6b6e9c1](https://github.com/diillson/chatcli/commit/6b6e9c1dbbf031956b117748b10ee7b67c932c58))
* **cli:** ajustando o tokens default OpenAI - preparando para gpt-4.1. ([9069759](https://github.com/diillson/chatcli/commit/9069759a4da93b0abaaaa762742a1f661c367484))
* **cli:** ajuste tokens e timeout request. ([23fb9ff](https://github.com/diillson/chatcli/commit/23fb9ffbd1943d19afadc5c322ea90aa6e35a695))
* **cli:** enhance cancel operation, model switching, and user contex… ([b44d604](https://github.com/diillson/chatcli/commit/b44d604d0dfe77c09c1980a3a1616205b3c5c236))
* **cli:** enhance cancel operation, model switching, and user context handling ([745624e](https://github.com/diillson/chatcli/commit/745624e0fa461b6d63ef5e4eb779ebd2f7fac34f))
* **cli:** ordenando pontuação. ([c8cb4a3](https://github.com/diillson/chatcli/commit/c8cb4a30a185c81a4f4580fe796ca260f7b8d209))
* **cli:** Refatoração da CLI, adição de testes e melhorias no processamento de comandos. ([82b8c8f](https://github.com/diillson/chatcli/commit/82b8c8fa9158a85654a5f2f20792c86cd723081a))
* **cli:** Refatoração da CLI, adição de testes e melhorias no processamento de comandos. ([ac6e70a](https://github.com/diillson/chatcli/commit/ac6e70a52c03ff7b627be1a6ec15343765e8b5b2))
* Correção de descrição de comandos e definição default alterada … ([3d3e096](https://github.com/diillson/chatcli/commit/3d3e09650c7b3580256b79af63bd1964976307a4))
* Correção de descrição de comandos e definição default alterada … ([95cfcd1](https://github.com/diillson/chatcli/commit/95cfcd16b8115765fb2a422ba2ef39cf9b014024))
* Correção de descrição de comandos e definição default alterada … ([8c6970d](https://github.com/diillson/chatcli/commit/8c6970d19f7b4b425e1ec8ed82d147987dc73764))
* Correção de descrição de comandos e definição default alterada para uso de stackspotAI. ([6d2ccf6](https://github.com/diillson/chatcli/commit/6d2ccf6b28e64792bbdcbac9eddcbb18d6a46dfa))
* Correção de descrição de comandos e definição default alterada para uso de stackspotAI. ([8112dce](https://github.com/diillson/chatcli/commit/8112dce6e6a0ee0a7adb7a88c95d874e75e0f7e4))
* Correção de descrição de comandos e definição default alterada para uso de stackspotAI. ([bae0138](https://github.com/diillson/chatcli/commit/bae0138987d35e12c2f8faf849d31513a365e86a))
* Correção de descrição de comandos e definição default alterada para uso de stackspotAI. ([dc333c0](https://github.com/diillson/chatcli/commit/dc333c056ce790624b448df1247edcd1e898c284))
* deixando terminal resposivo, removendo valor default para terminal. ([ca36fec](https://github.com/diillson/chatcli/commit/ca36fec8764651c6485b3e943008994a90ec1875))
* **docs:** Adicionando instalação via Go na Doc. ([8d7f4bb](https://github.com/diillson/chatcli/commit/8d7f4bb812b6bcb39cff820c74e12bd8810d8302))
* **docs:** Adicionando instalação via Go na Doc. ([c6406c1](https://github.com/diillson/chatcli/commit/c6406c1e38632abff4d4f33dc23639e7b113c04c))
* **docs:** Adicionando instalação via Go na Doc. ([0be3336](https://github.com/diillson/chatcli/commit/0be33364353508a979068d0cb0c6f302f73d2275))
* **docs:** pequenos ajustes no README.md ([a8dc9eb](https://github.com/diillson/chatcli/commit/a8dc9eba4f8202b785b7d42d515fb36e635c01c3))
* **docs:** UP mermaid, Documentação de Arquitetura. ([58341b2](https://github.com/diillson/chatcli/commit/58341b29644379323ae7df59d159f49de2495e0c))
* **docs:** UP mermaid, Documentação de Arquitetura. ([4c258f5](https://github.com/diillson/chatcli/commit/4c258f54945b29284ed473ecd0160f24688cdeb9))
* elevando timeout do response pooling LLM. ([c5e3796](https://github.com/diillson/chatcli/commit/c5e37965702bb96f67ca42f6a6f0099bc99f1e29))
* elevando timeout do response pooling LLM. ([f084060](https://github.com/diillson/chatcli/commit/f084060e5b44ad93e551fbc16b0b09e991ecdf3c))
* Enhance CLI Control, Agent Mode, and Cancellable Operations ([a6799f5](https://github.com/diillson/chatcli/commit/a6799f5952e2912ed0eaae730f883b54b7bf034c))
* enhance model handling and token limits for ClaudeAI and OpenAI ([6cd1548](https://github.com/diillson/chatcli/commit/6cd1548b330b587a3055677c47893d062956211b))
* enhance model handling and token limits for ClaudeAI and OpenAI ([62b5cce](https://github.com/diillson/chatcli/commit/62b5ccea671f82e78961fa6727dea5489c715ad5))
* Extendendo o retorno do comando GIT. ([b03e6ce](https://github.com/diillson/chatcli/commit/b03e6ce40b44dc5383d73c91e53beb8b1c07041d))
* introduce centralized catalog for model metadata and refactor LLM utilities ([811e00f](https://github.com/diillson/chatcli/commit/811e00ff201604ca08cb2f2b35c37e35713d4296))
* **lint:** depois de lint resolvido, removendo comentarios de debug ([b310bc1](https://github.com/diillson/chatcli/commit/b310bc1e25d53cf87cca7f6b9b5e84cf9846a8a8))
* **lint:** resolvendo lint sinalizado ([1e55571](https://github.com/diillson/chatcli/commit/1e55571a00afeeae31b6de24d30a16482dbe20f1))
* **main:** Melhorando a limpeza periódica de threads logo após criar a instância. ([b21086e](https://github.com/diillson/chatcli/commit/b21086e12d2b067e8fd42c1135b785466a37778c))
* **maxtokens:** resolvendo bug de maxtokens para apis da openai. ([3f443bd](https://github.com/diillson/chatcli/commit/3f443bd0e19160294001268220ecf4d09461e73d))
* **maxtokens:** resolvendo bug de maxtokens para apis da openai. ([40e27fe](https://github.com/diillson/chatcli/commit/40e27fe3f03f835ab437531f07864da65d400c7d))
* melhorando a captura de comandos citados pela LLM ([a62eae6](https://github.com/diillson/chatcli/commit/a62eae62a5a5843b815640ab965608969db6cede))
* melhorando o uso de contexto no Go com o modo agent ([3334cab](https://github.com/diillson/chatcli/commit/3334cabff52b048e420bb5ba8a4ae28b8a9c360b))
* model default, update model gpt. ([f123c82](https://github.com/diillson/chatcli/commit/f123c82ec7e84859ce47bb1aacb4bd35cd31b103))
* modificando o implementador de contexto para AI de pipe | para Maior &gt;. ([e7ab822](https://github.com/diillson/chatcli/commit/e7ab822b2ebf2031b67d03e553ec8a5193fcccaa))
* **README:** Adicionado Implementação de execução direta de comandos ([a33896c](https://github.com/diillson/chatcli/commit/a33896cdcc4f4222e415aacf2d8245bf74606a88))
* **README:** Adicionado Implementação de execução direta de comandos ([5e65ff1](https://github.com/diillson/chatcli/commit/5e65ff1ea4013c505b38e569c92b6c761cc91da5))
* **readme:** adicionando novo comando e extensão ao modo agente. ([d567295](https://github.com/diillson/chatcli/commit/d5672958003de08be08389d1848a0920e680e4bf))
* Reduzindo tempo entre os GET de callback do provider stackspot, e ajustando a forma de salvar o historico de mensagens para tudo que é digitado na CLI. ([bd36a48](https://github.com/diillson/chatcli/commit/bd36a48504ba172acda2a6ebb08e451c48f3019e))
* Reduzindo tempo entre os GET de callback do provider stackspot, e ajustando a forma de salvar o historico de mensagens para tudo que é digitado na CLI. ([394d4b9](https://github.com/diillson/chatcli/commit/394d4b908151fdcf307fe1c1ebd4705077842c8f))
* refactor message building to avoid prompt duplication and add s… ([92a5c8b](https://github.com/diillson/chatcli/commit/92a5c8b284fe95bb4baa2841a001d57f0f8db5ac))
* refactor message building to avoid prompt duplication and add system-level instructions ([8f140b3](https://github.com/diillson/chatcli/commit/8f140b3035466595bbd6313459aa4627494908bc))
* Refactor. ([1633125](https://github.com/diillson/chatcli/commit/16331254e710bb4dbee34cce505dd4aeb721c949))
* Refactor. ([1bcb23c](https://github.com/diillson/chatcli/commit/1bcb23c7eb47a3546cbc88bc6b0ede3466b9ea2c))
* Refatoração de todo codigo, com validação da LLM para fins de p… ([ab2bcd9](https://github.com/diillson/chatcli/commit/ab2bcd9bd39e44432a11f156b986f83ba61f27b8))
* Refatoração de todo codigo, com validação da LLM para fins de produção, Readme.md redigito pela mesma também. ([1521502](https://github.com/diillson/chatcli/commit/1521502a4bf4465ad40ea497c53c12bddb47b511))
* Refatoração de todo codigo, com validação da LLM para fins de produção, Readme.md redigito pela mesma também. ([3e0ab3f](https://github.com/diillson/chatcli/commit/3e0ab3f184373410ea396ea687b5c99454acd374))
* refatoração do codigo para atender novos argumentos de switch e atualização do Readme.md ([e94b126](https://github.com/diillson/chatcli/commit/e94b12640bfed8790d1104c07db0ae0d1513d47a))
* refatoração do codigo para atender novos argumentos de switch e… ([39a3a6a](https://github.com/diillson/chatcli/commit/39a3a6ae3dc4b8c83897bf9ee522a08e3caf5ea3))
* refatorando cli.go ([1e411be](https://github.com/diillson/chatcli/commit/1e411beb145e1398c4fda92285804b9f76f5fd0e))
* reimplementação completa do método executeCommandsWithOutput ([ee722e8](https://github.com/diillson/chatcli/commit/ee722e8be2548a88f8ea1e7ad3b67c9270f9e9df))
* **smart:** Implementação de execução direta de comandos ([ccc0a08](https://github.com/diillson/chatcli/commit/ccc0a081f72ddd4db10791de6efc09448afa0e46))
* Update getshell, adicionando shell windows. ([4816c98](https://github.com/diillson/chatcli/commit/4816c980af0639a0666a82e986352c50962ecd77))
* Update getshell, adicionando shell windows. ([c090abc](https://github.com/diillson/chatcli/commit/c090abce1f313acbbc8d138af5923bfea9565a9f))
* Update mensagem de usuario. ([031007f](https://github.com/diillson/chatcli/commit/031007f83527ebd3e6252ee34e68e667f0ed3962))
* **version:** adiciona comando para verificar e exibir informações de versão ([5870eac](https://github.com/diillson/chatcli/commit/5870eac23dec47ea8fbed5de0ebbf5f20b6ebc98))


### Bug Fixes

* **agent:** ajustando comentarios da func isLikelyInteractiveCommand. ([7443b3b](https://github.com/diillson/chatcli/commit/7443b3b6da66a54c3c621ceb39924d2850a1d0d0))
* **agent:** extendendo pathners interativos ([2444ae6](https://github.com/diillson/chatcli/commit/2444ae635b81d2c6e39ee1bec98a5f72d8604405))
* **agent:** melhorar a detecção de comandos interativos para evitar a identificação incorreta de trechos de código. ([e214419](https://github.com/diillson/chatcli/commit/e2144197eb020342f0f7b5c7b88a8c63c6d6998a))
* **agent:** melhorar tratamento de scripts multilinhas e detecção de comandos interativos ([6f05715](https://github.com/diillson/chatcli/commit/6f057154931b493fe4019fde03b120956abf31a1))
* **agentMode:** resolver problemas com OpenAI Assistant no modo agente ([763e86f](https://github.com/diillson/chatcli/commit/763e86f127784274c8236990366009708d8e9077))
* **cli:** improve file command regex to handle quoted paths ([08f9ce8](https://github.com/diillson/chatcli/commit/08f9ce86ed6293bc45fb2eb7a0cf0d6cb90472a8))
* **cli:** improve file command regex to handle quoted paths ([1b5e022](https://github.com/diillson/chatcli/commit/1b5e022941a05a1efd2f5a1f7bcb5a26757f15cb))
* corrige bug no Y ([b49c430](https://github.com/diillson/chatcli/commit/b49c4308a3ea6e286f004eb8221835ddf3c795e2))
* corrigindo erro no lint, nova linha redundante. ([d314362](https://github.com/diillson/chatcli/commit/d314362c9e63b358320d20d6e1416786c5d12870))
* corrigir referências e fluxo do workflow de release ([ca9150e](https://github.com/diillson/chatcli/commit/ca9150e0bcc58ae6c7bfdc1883a7b48099641b82))
* corrigir referências e fluxo do workflow de release ([334d298](https://github.com/diillson/chatcli/commit/334d2988af29b569f0f50372da67339ab543d7c6))
* **doc:** expondo badges. ([c507ffd](https://github.com/diillson/chatcli/commit/c507ffd795456214cba054eebc08a6fcab2405ea))
* **fileCommand:** add progress tracking and file count during directo… ([0f7581e](https://github.com/diillson/chatcli/commit/0f7581e6c21869864caedf9aa883601c4fa4e703))
* **fileCommand:** add progress tracking and file count during directory scanning ([d04ebc2](https://github.com/diillson/chatcli/commit/d04ebc2a3dd8eed47de458d1aa660503e28f76ec))
* **gemini:** handle error when closing response body ([f2d2701](https://github.com/diillson/chatcli/commit/f2d2701b65239cbd8edd8686eecd802f4ae59d6a))
* **gemini:** handle error when closing response body ([14d6622](https://github.com/diillson/chatcli/commit/14d6622fc7c9ff1c43c0e4472930b210e6f7e9ff))
* **gemini:** simplify max tokens assignment logic ([7ae5b9a](https://github.com/diillson/chatcli/commit/7ae5b9a8dc03679cc480a6077f145c0a77e28df8))
* **gemini:** simplify max tokens assignment logic ([954df53](https://github.com/diillson/chatcli/commit/954df532b847f26f1c0dfe7cb43013c1e3dd2f71))
* **main:** releaseplease ([88a73d6](https://github.com/diillson/chatcli/commit/88a73d6603bb8bd6957886560020b34357152faf))
* **main:** releaseplease ([70b6c40](https://github.com/diillson/chatcli/commit/70b6c404f6ea950c86fe189bfe99aa9a505b3a6b))
* **main:** simplify version startup comment ([0431ef7](https://github.com/diillson/chatcli/commit/0431ef7c8defe7a9c029a4682a07bba8a9886e5a))
* **main:** simplify version startup comment ([8498df8](https://github.com/diillson/chatcli/commit/8498df8a5b1bd34b90b880c8c4c887488008cfc4))
* melhorando o gestor de versão. ([15379aa](https://github.com/diillson/chatcli/commit/15379aa9cde99f52396c76173b73607277978263))
* Melhorar comparação semântica de versões e gestão de recursos ([eed3722](https://github.com/diillson/chatcli/commit/eed3722fbbc218bfc4c5bbd2c5dc5d3d3f25dab0))
* **modeOneShot:** centralize one-shot handling in `HandleOneShotOrFatal` ([39466e0](https://github.com/diillson/chatcli/commit/39466e0bf16ec5bfb7f9ca2249090a4e35c5f0f0))
* **modeOneShot:** centralize one-shot handling in `HandleOneShotOrFatal` ([17cde1e](https://github.com/diillson/chatcli/commit/17cde1e3fc8b3ac12910b009f6eff28a1abdb224))
* **modeOneShot:** correct stdin example syntax in help message ([993b2ae](https://github.com/diillson/chatcli/commit/993b2ae39fc0a180ec6c9c7bcf5f2923ec27593c))
* **modeOneshot:** extend one-shot mode to support stdin input ([999ccdd](https://github.com/diillson/chatcli/commit/999ccdd3d2e36543efb931e81cfca30712e956fb))
* **modeOneshot:** extend one-shot mode to support stdin input ([22acb64](https://github.com/diillson/chatcli/commit/22acb642b5d5f9b4e8c89696ffdc3bb341154cfd))
* **modeOneShot:** normalize -p/--prompt flags without values in one-s… ([7ae143b](https://github.com/diillson/chatcli/commit/7ae143b767ca59c217477f69545054d84be57f7b))
* **modeOneShot:** normalize -p/--prompt flags without values in one-shot mode ([10e24da](https://github.com/diillson/chatcli/commit/10e24dafd8a3074f73a07549b1128af1e2e1f745))
* **modeOneShot:** streamline input validation and error messaging ([a789298](https://github.com/diillson/chatcli/commit/a789298a36360f7d2bb711613bf4f4f5d07e1136))
* nova mensagem de prompt para Assistante API OPENAI. ([19232d6](https://github.com/diillson/chatcli/commit/19232d6e9bc4da33ce86358911057bb71dfb39c7))
* Resolve problemas de entrada e execução de Multiplos comandos, opção "a" no modo de agente ([be6bc51](https://github.com/diillson/chatcli/commit/be6bc510bce9570dec59d8c612ea67320d2794e6))
* Rollback mensagem de systema para comandos api OPENAI ASSISTANT. ([f514a47](https://github.com/diillson/chatcli/commit/f514a47a96d15426f4fc8467964a27cea9b3d836))
* Rollback Workflows. ([6f5176f](https://github.com/diillson/chatcli/commit/6f5176f2422cb3e7777abcdcaeb3cf69a8361ed6))
* Rollback Workflows. ([a4c68a3](https://github.com/diillson/chatcli/commit/a4c68a332675a07d043fa0aaddfa7f70142926d5))
* Sanitizando o código ([6c55e1c](https://github.com/diillson/chatcli/commit/6c55e1c0575fb2ca34027c947a845f936cc02fc9))
* Sanitizando o código ([3ff7c76](https://github.com/diillson/chatcli/commit/3ff7c76ffb0ba144578a71cec3b179a72c6b5452))
* **scope:** fix bug ([7cdc360](https://github.com/diillson/chatcli/commit/7cdc360530e16a6a5e6c4f9fd6ae168c098e449b))
* **scope:** fix bug workflow releaseplease. ([96f5978](https://github.com/diillson/chatcli/commit/96f59782568219e8639e97874d1943b95e1888e6))
* **scope:** fix bug workflow releaseplease. ([a759d50](https://github.com/diillson/chatcli/commit/a759d501f384c51d89557c3fbd7fe4bcb9af6032))
* test release please workflow ([798495f](https://github.com/diillson/chatcli/commit/798495f24b7124088c2d7ec8ebe4ba3ac75e6836))
* test release please workflow ([486bd8c](https://github.com/diillson/chatcli/commit/486bd8c4624834b2e2ab69f14e2d5aba89aeba54))
* test release please workflow ([a7a3455](https://github.com/diillson/chatcli/commit/a7a34556dbb5b67d5c708797b3810cdb61dfc80f))
* test release please workflow ([1125fe5](https://github.com/diillson/chatcli/commit/1125fe58f0c486fe44f29cdf937634c8a51d4b8c))
* test release please workflow ([7605f02](https://github.com/diillson/chatcli/commit/7605f02772b9b31d574be76427ec554d360cc672))
* teste fluxo de sync automático na develop. ([9fffa16](https://github.com/diillson/chatcli/commit/9fffa16523e455dcd6c541a1fd90227f18593c94))
* teste fluxo de sync automático na develop. ([80ac765](https://github.com/diillson/chatcli/commit/80ac765b8ee4571310e775d945a407c9861b2485))
* teste fluxo de sync automático na develop. ([ade51ec](https://github.com/diillson/chatcli/commit/ade51ecfdb01e6351ca5f3d001fb3af315092b29))
* teste fluxo de sync automático na develop. ([579c6f6](https://github.com/diillson/chatcli/commit/579c6f658b49c613661140ffab7cc7adb90bd02f))
* unificar processamento de arquivos no comando [@file](https://github.com/file) ([9b10dd1](https://github.com/diillson/chatcli/commit/9b10dd1690f2dbf2ea30644d85d7047c0e6f0abd))
* update comment in GetHomeDir function to trigger release ([af04b11](https://github.com/diillson/chatcli/commit/af04b11615828f98917d6383962470445884ba1c))
* Update message version. ([5f49fec](https://github.com/diillson/chatcli/commit/5f49fecf519837fb3602fce374d30f4ccbc5134f))
* Update message version. ([c69df28](https://github.com/diillson/chatcli/commit/c69df286153e87b0861a419a05e61d15cd890327))
* **version:** correct update command to include 'v' prefix in version string ([518e88e](https://github.com/diillson/chatcli/commit/518e88e5064e835c01802a899dbc98ac3a74a4a1))
* **version:** correct update command to include 'v' prefix in version… ([5a09c42](https://github.com/diillson/chatcli/commit/5a09c426408e0abf026a3d9d8fe8aa054276271d))
* **version:** improve version detection and update prompts ([6ed8d2a](https://github.com/diillson/chatcli/commit/6ed8d2a5b0ec493c211b372dd8d8e0cbae4fb23e))
* **version:** improve version detection and update prompts ([e50be13](https://github.com/diillson/chatcli/commit/e50be1324de4e0c5822e631664c62910a429702d))

## [1.17.4](https://github.com/diillson/chatcli/compare/v1.17.3...v1.17.4) (2025-08-25)


### Bug Fixes

* **scope:** fix bug ([7cdc360](https://github.com/diillson/chatcli/commit/7cdc360530e16a6a5e6c4f9fd6ae168c098e449b))
* **scope:** fix bug workflow releaseplease. ([96f5978](https://github.com/diillson/chatcli/commit/96f59782568219e8639e97874d1943b95e1888e6))
* **scope:** fix bug workflow releaseplease. ([a759d50](https://github.com/diillson/chatcli/commit/a759d501f384c51d89557c3fbd7fe4bcb9af6032))
* test release please workflow ([a7a3455](https://github.com/diillson/chatcli/commit/a7a34556dbb5b67d5c708797b3810cdb61dfc80f))
* test release please workflow ([1125fe5](https://github.com/diillson/chatcli/commit/1125fe58f0c486fe44f29cdf937634c8a51d4b8c))
* test release please workflow ([7605f02](https://github.com/diillson/chatcli/commit/7605f02772b9b31d574be76427ec554d360cc672))

## [1.17.3](https://github.com/diillson/chatcli/compare/v1.17.2...v1.17.3) (2025-08-25)


### Bug Fixes

* **agent:** ajustando comentarios da func isLikelyInteractiveCommand. ([7443b3b](https://github.com/diillson/chatcli/commit/7443b3b6da66a54c3c621ceb39924d2850a1d0d0))
* **main:** releaseplease ([88a73d6](https://github.com/diillson/chatcli/commit/88a73d6603bb8bd6957886560020b34357152faf))
* **main:** releaseplease ([70b6c40](https://github.com/diillson/chatcli/commit/70b6c404f6ea950c86fe189bfe99aa9a505b3a6b))
* **main:** simplify version startup comment ([0431ef7](https://github.com/diillson/chatcli/commit/0431ef7c8defe7a9c029a4682a07bba8a9886e5a))
* **main:** simplify version startup comment ([8498df8](https://github.com/diillson/chatcli/commit/8498df8a5b1bd34b90b880c8c4c887488008cfc4))

## [1.17.2](https://github.com/diillson/chatcli/compare/v1.17.1...v1.17.2) (2025-08-25)


### Bug Fixes

* **cli:** improve file command regex to handle quoted paths ([08f9ce8](https://github.com/diillson/chatcli/commit/08f9ce86ed6293bc45fb2eb7a0cf0d6cb90472a8))
* **cli:** improve file command regex to handle quoted paths ([1b5e022](https://github.com/diillson/chatcli/commit/1b5e022941a05a1efd2f5a1f7bcb5a26757f15cb))

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
