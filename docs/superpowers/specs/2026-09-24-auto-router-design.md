# cpa-plugin-auto-router — design

Data: 2026-09-24. Estado: rascunho para revisão do operador.

## Objetivo

Um plugin nativo do CLIProxyAPI que publica um modelo virtual, `auto-router`,
no catálogo do proxy (`/v1/models`). Um pedido para `auto-router` é
classificado por Jev (TypeSafe System One, via OpenRouter) em **categoria de
tarefa** e **dificuldade**; uma **tabela local de benchmarks** escolhe o
modelo concreto e o nível de thinking; o host executa o pedido no modelo
escolhido pelo caminho normal (credenciais, cooldown, retry, usage, log).

Referência de forma: <https://route.jev.works/> — opções fixas, uma pergunta
`choice`, pick + probabilidade por opção + confiança, threshold, fallback
seguro, histórico de decisões. Aqui as "opções" do Jev são rótulos de tarefa,
não modelos: a comparação entre modelos é determinística, local e auditável.

Fora de escopo nesta versão: Claude Code e Codex CLI como clientes (v1 atende
OMP e Hermes), classificador local (SemIf), persistência do estado de sessão,
UI no Management Center.

## Decisões fechadas com o operador

| Tema | Decisão |
|---|---|
| Forma | Plugin nativo (`.so`, Go/cgo) com `model_registrar` + `model_router` + `executor` self. Não sidecar. |
| Clientes v1 | OMP (Responses) e Hermes (chat-completions). |
| O que sai do host para o Jev | Trecho da última mensagem do usuário (≤ 1.500 chars) + sinais locais. Nunca system prompt, tool results, anexos. |
| Re-roteamento | Reavalia a cada turno; **só escala**, nunca rebaixa. Sem escalada, mantém o modelo e o thinking gravados. |
| Desempate no tier | Maior score nos benchmarks da categoria. Custo só desempata empate real (dentro da margem publicada). |
| Jev | **Uma** chamada por turno com várias perguntas ortogonais. Sem passes encadeados. |
| Thinking | Mapeado direto da dificuldade (`low/high/xhigh/max`), igual para todo modelo do tier. O proxy clampa quando o modelo não suporta. |
| Visão | Não é categoria. Pedido com imagem filtra localmente os candidatos com `vision: true`. |
| Sem benchmark | Modelo sem score na categoria nunca vence; entra só como fallback. |
| Políticas pessoais | **Nenhuma.** A decisão é 100% benchmark (Fable, Sonnet etc. concorrem como qualquer outro). Exclusões só por capacidade: modelos de imagem, aliases abliterated, `codex-auto-review`. |
| Repositório | Repo próprio (`cpa-plugin-auto-router`), fora do nix, como os plugins da store. |

## Fluxo por pedido

```
cliente → POST /v1/responses|/v1/chat/completions  model=auto-router
  │
  ├─ model.route (plugin)
  │    1. RequestedModel != auto-router → Handled:false (não é conosco)
  │    2. chave de sessão (mesma ordem do host: X-Session-ID, prompt_cache_key,
  │       X-Session-Affinity, hash da primeira mensagem)
  │    3. sessão já em `extremo` → decisão = gravada; pula o Jev
  │    4. body sem mensagem nova de usuário (só tool results) → decisão = gravada
  │    5. jev_decide(estado, perguntas)   ← 1 chamada, timeout curto
  │    6. escolha(tabela, categoria, dificuldade, tem_imagem, disponíveis)
  │    7. aplica regra de escalada contra o estado da sessão; grava
  │    8. log JSON da decisão; guarda decisão por request-id
  │    → Handled:true, TargetKind:self
  │
  └─ executor.execute / execute_stream (plugin)
       host.model.execute(_stream) { model: "<modelo>(<thinking>)",
                                     entry=exit=protocolo de entrada,
                                     host_callback_id }   ← host pula este roteador
       forward do stream chunk a chunk; header X-Auto-Router na resposta
```

O executor se declara com `executor_model_scope: static`,
`executor_input_formats: [responses, chat-completions]` e saída igual à
entrada. O `host.model.*` recebe `EntryProtocol == ExitProtocol == SourceFormat`
do pedido: o corpo passa **sem tradução** pelo plugin; a tradução para o
upstream é do host, como hoje.

## Classificação (Jev)

Estado enviado (`state`):

```json
{
  "context": "Pedido a um proxy de LLMs. Classifique a tarefa que o usuário está pedindo.",
  "item": "<últimos ≤1500 chars da última mensagem de usuário>",
  "signals": {"tools": 12, "images": 0, "messages": 37, "format": "responses"}
}
```

Perguntas (`questions`), todas na mesma chamada:

| nome | tipo | opções |
|---|---|---|
| `category` | choice | `webdev`, `backend`, `agentic-terminal`, `debugging`, `review`, `spec-design`, `writing`, `extraction`, `math-data` |
| `difficulty` | choice | `trivial`, `routine`, `hard`, `extreme` |

As probabilidades por opção que o `choice` devolve são gravadas no log; a v1
usa argmax. Mistura ponderada de benchmarks (60 % webdev / 40 % backend) fica
como upgrade se o argmax se mostrar instável em categorias vizinhas.

Confiança (`answers.<q>.confidence`) abaixo de **0,6**:

- `difficulty` incerta → `routine` em sessão nova; em sessão existente mantém
  o gravado. Dúvida nunca escala.
- `category` incerta → ranking geral (AA Intelligence), sem benchmark de
  categoria.

Jev indisponível, timeout ou resposta inválida → mesmo default, `reason:
jev-unavailable`, **o pedido segue**. O roteador é fail-open; nunca bloqueia.

Jev não é fronteira de segurança: texto injetado no prompt pode mover a
decisão. O pior caso é rotear para um modelo mais caro; nada destrutivo
depende do rótulo.

## Tabela de decisão

### dificuldade → tier + thinking

| dificuldade | tier | thinking |
|---|---|---|
| `trivial` | flash | `low` |
| `routine` | mid | `high` |
| `hard` | top | `xhigh` |
| `extreme` | top | `max` |

O thinking sai como sufixo no nome do modelo (`gpt-6-astra(xhigh)`). O host
(`internal/thinking/validate.go`) converte nível ↔ budget e clampa para o
nível mais próximo quando o modelo não suporta (`max` vira `high` em Kimi,
por exemplo); configuração vinda de sufixo é clampada, nunca rejeitada.

### categoria → benchmarks que pesam

| categoria | benchmarks (ordem de peso) |
|---|---|
| `webdev` | Arena WebDev |
| `backend` | SWE-bench Verified, DeepSWE |
| `agentic-terminal` | Terminal-Bench, AA Coding Agent Index |
| `debugging` | SWE-Atlas-QnA, Terminal-Bench |
| `review` | AA Intelligence |
| `spec-design` | AA Intelligence, Arena (texto) |
| `writing` | AA Intelligence |
| `extraction` | nenhum — sempre tier flash, vence o mais barato |
| `math-data` | AA Intelligence (componentes GPQA/AIME) |

Categoria sem score disponível para os candidatos do tier → AA Intelligence.

### Escolha

```
candidatos = modelos da tabela com tier == tier(dificuldade)
           ∖ exclusões por capacidade
           ∩ (tem_imagem ? vision == true : todos)
           ∩ disponíveis no host (AvailableProviders / cooldown)
ranqueados = candidatos com score no primeiro benchmark da categoria
           (sem score no primeiro → tenta o próximo benchmark da lista)
vencedor   = maior score; empate (|Δ| ≤ margem publicada do benchmark)
             → menor custo (input + output, metadata do discovery)
sem ranqueado disponível → fallback: candidatos sem score, mais barato primeiro
sem candidato nenhum no tier → sobe um tier (nunca desce)
```

"Sem score" é diferente de "score zero": um modelo só entra no ranking se a
tabela tiver `{value, source, date}` para aquele benchmark.

### Formato da tabela (`models.yaml`, ao lado do `.so`)

```yaml
benchmarks:
  arena-webdev:   {margin: 16,  source: "https://arena.ai/leaderboard/code"}
  swe-bench:      {margin: 1.0, source: "https://www.swebench.com/"}
  terminal-bench: {margin: 2.0, source: "https://artificialanalysis.ai/..."}
  aa-intelligence:{margin: 1.0, source: "https://artificialanalysis.ai/leaderboards/models"}

models:
  gpt-6-astra:
    tier: top
    vision: true
    cost: {input: 10, output: 50}          # USD / M tokens, discovery 2026-09-12
    scores:
      arena-webdev:   {value: 1800, date: 2026-09-11}
      terminal-bench: {value: 56,   date: 2026-09-09}
      aa-intelligence:{value: 51,   date: 2026-09-09}   # effort high, não max
  gpt-5.6-sol:
    tier: top
    vision: true
    cost: {input: 2, output: 10}
    scores:
      deepswe:        {value: 72,   date: 2026-09-09}
      terminal-bench: {value: 37,   date: 2026-09-09}
  glm-5.3-flash:
    tier: flash
    vision: false
    cost: {input: 0.15, output: 0.5}
    scores:
      aa-intelligence:{value: 42,   date: 2026-09-09}
  gpt-5.6-luna:
    tier: mid
    vision: true
    cost: {input: 0.2, output: 1.2}
    scores: {}                              # sem benchmark → só fallback
```

Regras de carga: entrada de score sem `date` ou sem `benchmark` declarado
rejeita o arquivo inteiro (o plugin sobe com a tabela anterior e loga erro).
Recarga por `mtime` a cada decisão (um `stat`), sem watcher. Os valores
iniciais vêm de
`home/common/ai/resources/agents/skills/model-routing/references/benchmarks-cliproxy.md`
(nix, 2026-09-12) e são refrescados por edição do YAML; o plugin nunca
consulta leaderboards sozinho.

O `tier` de cada modelo é declarado na tabela, não inferido de preço. Esboço
inicial: flash = `glm-5.3-flash`, `deepseek-v4-flash`; mid = `gpt-5.6-luna`,
`gpt-5.6-terra`, `glm-5.3`, `kimi-k3`, `claude-sonnet-5`, `deepseek-v4-pro`;
top = `gpt-6-astra`, `gpt-5.6-sol`, `claude-opus-5`, `claude-fable-5-1`. A
lista é decisão de tabela, revisável sem recompilar.

## Sessão e escalada

Estado por sessão, em memória: `{difficulty, model, thinking, updated_at}`.
TTL 1 h (mesmo `session-affinity-ttl` do proxy), teto de 65 536 entradas com
descarte do mais antigo. Perde no restart: uma sessão viva é reclassificada do
zero uma vez. `ponytail:` persistir só se isso incomodar na prática.

Regra por turno:

| situação | ação | `reason` |
|---|---|---|
| sessão nova | classifica, escolhe, grava | `new` |
| dificuldade ≤ gravada | mantém modelo e thinking | `keep` |
| escalou, mesmo tier | mantém modelo, sobe thinking | `escalate-thinking` |
| escalou, tier acima | escolhe de novo com a categoria deste turno | `escalate-tier` |
| imagem e o modelo gravado não tem visão | melhor modelo **com visão do mesmo tier**, mesma categoria e thinking | `vision-swap` |
| já em `extreme` | não chama o Jev | `keep` |
| Jev falhou | default acima | `jev-unavailable` |
| modelo gravado indisponível | escolhe de novo no mesmo tier | `fallback` |

Mudança de categoria no meio da sessão **não** troca modelo por si só. Manter
o modelo preserva o cache de prompt, que domina o custo real.

## Observabilidade

- Uma linha JSON por decisão no log do proxy: `session` (hash curto),
  `category` + probabilidade, `difficulty` + probabilidade, `confidence`,
  `tier`, `model`, `thinking`, `reason`, `jev_ms`. Equivale ao "routing
  history" do route.jev.works.
- Header de resposta `X-Auto-Router: <model>(<thinking>);<reason>` via
  `ExecutorResponse.Headers`, para ver no cliente quem respondeu sem abrir log.
- `plugin.register` expõe `ConfigFields`: `enabled`, `jev_api_key_env`,
  `jev_base_url`, `jev_model`, `confidence_threshold` (0,6), `table_path`,
  `snippet_chars` (1500), `jev_timeout` (2 s).

## Segurança e privacidade

- Chave do Jev lida de variável de ambiente nomeada na config, nunca do YAML.
- URL do Jev: https sempre; http só loopback/LAN (mesma política do plugin
  `jev` do Hermes).
- Sai do host apenas o trecho da última mensagem do usuário e contadores. Sem
  system prompt, sem tool results, sem imagens, sem headers.
- O plugin não vê nem guarda credenciais de upstream: usa `host.model.*`.
- Falha do roteador nunca bloqueia: default + log.

## Testes

- Núcleo de decisão (`decide(tabela, resposta_jev, estado, sinais)`) é puro e
  testado com casos gravados: empate por custo, sem score → fallback, escalada
  só sobe, `vision-swap`, categoria incerta, Jev fora, subida de tier quando o
  tier está vazio.
- Loader da tabela: rejeita score sem data, benchmark não declarado, tier
  inválido.
- Smoke real contra o proxy: `curl` para `auto-router` com um pedido trivial e
  um difícil; conferir `X-Auto-Router` e a linha de log.

## Verificações antes de codar

Empíricas; nenhuma muda o desenho, só confirma o caminho:

1. Compilar e carregar o exemplo oficial `examples/plugin/claude-web-search-router`
   no binário 7.2.159 (ABI 1, schema 6) com Go 1.27.1 do host.
2. Sufixo `modelo(nivel)` via `host.model.execute` cai em `ValidateConfig` com
   `fromSuffix=true` (caminho clamp, não erro).
3. Qual sinal de sessão OMP e Hermes mandam de fato (`prompt_cache_key`?
   header?); confirmar que o hash da primeira mensagem estabiliza quando não há.
4. `ExecutorResponse.Headers` chega ao cliente em stream e não-stream.
5. `host.model.execute_stream` com `entry == exit == responses` devolve o SSE
   de Responses intacto (reasoning items, tool calls).

## Fontes

- Seam do roteador: `sdk/api/handlers/handlers_routing.go` (`applyModelRouter`,
  `providersForExecution`), `internal/pluginhost/model_router.go`.
- Contrato: `sdk/pluginapi/types.go` (`ModelRouteRequest/Response`,
  `ExecutorResponse`, `HostModelExecutionRequest`), `sdk/pluginabi/types.go`.
- Thinking: `internal/thinking/suffix.go`, `internal/thinking/validate.go`.
- Sessão: `sdk/cliproxy/auth/selector.go` (`ExtractSessionID`, ordem de sinais).
- Exemplo com forward de stream: `examples/plugin/claude-web-search-router/go/`.
- Docs: <https://help.router-for.me/plugin/model-router>,
  `/plugin/model-registrar`, `/plugin/executor`, `/plugin/host-callbacks`.
- Jev: plugin `jev` do Hermes (`client.py`, política de URL e forma do wire).
- Benchmarks iniciais: `model-routing/references/benchmarks-cliproxy.md` (nix,
  2026-09-12).
