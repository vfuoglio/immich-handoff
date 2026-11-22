> [!NOTE]
> Esta página foi traduzida automaticamente e ainda não foi revista por uma pessoa. As imagens mostram a interface em inglês. Em caso de dúvida, vale a versão em inglês.

# Orientar-se na página

A página tem quatro vistas e uma barra de pesquisa que faz o que lhe pede. Esta é a visita de cinco minutos: onde estão as coisas, o que significam as palavras e o que lhe mostra a cena ao vivo.

![A Visão geral: o que o espera, a cena ao vivo e os números da semana](../../images/dashboard-owner.png)

## Primeiro: as etiquetas

O Handoff funciona com as **etiquetas** do Immich, e as etiquetas estão **desativadas por predefinição**, para cada pessoa em separado. Se as suas estiverem desativadas, a página mostra um aviso com um botão **Ativar as Tags** (também o pode fazer no Immich: Definições da conta, Funcionalidades, **Etiquetas**). Os remetentes precisam delas para marcar fotos, os destinatários para ver a etiqueta da caixa de entrada e filtrar por ela. Ativá-las não chega: **recarregue o separador ou a janela do Immich depois**, ou as opções de etiquetas não aparecem (na aplicação do telemóvel, feche-a e abra-a de novo).

## As quatro vistas

A barra de cima tem quatro vistas, numa pílula ao centro (num telemóvel é uma barra em baixo, com um ícone cada). O endereço lembra a vista (`#services`), por isso pode guardá-la nos favoritos, e a página volta a ela depois de uma ação.

- **Visão geral.** A data e uma saudação, a barra de pesquisa, depois **Precisa de você**, a **cena ao vivo** e **Esta semana**, uma debaixo da outra. O cartão **Começar** fica no fim até acabar com ele.
- **Serviços.** Cada serviço para onde envia, de onde recebe, que oferece ou a que se pode juntar. Se pode criar mais do que serviços, um seletor em cima alterna entre os seus serviços, caixas de entrega, salas de cliente, molduras e álbuns partilhados.
- **Atividade.** O que aconteceu, a última execução e **Sincronizar agora**.
- **Definições.** O que configura uma vez e depois esquece, em categorias (Notificações, Fotos, A partilhar, Ecrãs). A vista só existe se houver algo ativado para si.

### Precisa de você

Tudo o que espera uma pessoa vem primeiro, o mais urgente em cima, cada coisa com o seu botão. Quando nada espera, di-lo.

- **Conexão perdida** e **Removido pelo proprietário**: **Limpar**, que apaga o que a aplicação criou na sua conta Immich (depois pode juntar-se de novo).
- **Desconectado** (um serviço que oferece): **Reconectar**.
- **Pedido**: alguém quer enviar para um serviço que oferece. **Aprovar** ou **Recusar**; um pedido responde-se aqui e em mais lado nenhum.
- **Pausado**: um serviço que pausou, ou o seu próprio envio (**Retomar**), ou um serviço que o proprietário pausou (nada para carregar).
- **Limite**: um remetente atingiu o limite diário, ou você (veja [os limites diários](../../guides/automate/controls.md)).

### Esta semana

Um pequeno gráfico com um ponto por dia (dias UTC, hoje à direita) de quantas fotos foram entregues nos últimos sete dias: as fotos que **enviou** e as que **chegaram aos serviços que oferece**. Conta o que a aplicação registou das pessoas que se juntaram a partir da página, por isso os remetentes configurados no `.env` ou num ficheiro de rotas não aparecem. Ao lado, **Esperando para seguir** (fotos que marcou e ainda têm de partir) e **Na sua caixa de entrada** (o que chegou e ainda não está arrumado) atualizam-se sozinhos sempre que uma entrega termina.

### Começar

O cartão ativa as etiquetas, ajuda-o a juntar-se a um serviço ou a oferecer um e a entregar uma primeira foto, e sugere mais passos que combinam com o que faz (convidar alguém, uma notificação). Cada passo tem **Mostra-me** e um **?** para o seu guia, e um anel mostra quanto já fez. Desaparece quando acaba ou quando carrega em **Ocultar este guia**.

## A cena ao vivo

![A cena ao vivo: uma via para cada remetente e uma porta para a biblioteca](../../images/live-scene.png)

Logo abaixo da barra de pesquisa, **Ao vivo** mostra o que se passa enquanto se passa. Cada serviço em que participa tem um palco: uma **via** para cada remetente e uma porta para a biblioteca que recebe. Um quadrado de cor brilhante percorre a via do remetente até à porta e estende-se um anel onde aterra; quando é você a enviar, um quadrado com contorno e uma seta sai antes da sua porta. O contador **chegaram enquanto você olha** sobe para cada um. Os quadrados são só quadrados: a cena nunca mostra uma foto nem um nome de ficheiro, e só entregas reais, nunca inventadas.

Uma via também diz por palavras em que estado está, para que a cor nunca seja o único sinal: **Em andamento**, **Pausado pelo proprietário** ou **Pausado pelo remetente** (via tracejada), **Limite diário atingido** com quantas esperam e até quando (âmbar), **Conexão perdida** (vermelho, com uma cruz). Um breve registo sob os palcos lista as últimas coisas numa frase cada, e um leitor de ecrã recebe a mesma frase.

Mantém-se calma: nada se mexe enquanto nada acontece, só voam alguns quadrados de cada vez, e com movimento reduzido ou um separador oculto os quadrados são omitidos e só mudam as etiquetas, o contador e o registo. Se a ligação à aplicação cair, a cena esmorece e di-lo, e depois põe-se em dia sozinha.

![O separador Serviços: um serviço para onde envia, aberto no sítio, e um a que se pode juntar](../../images/services-sender.png)

Cada serviço é uma linha: a sua inicial, o nome, o que é, um estado como Pausado ou Limite, e o número que importa (fotos à espera, ou na sua caixa de entrada). Carregue nela para a abrir no sítio e ver as suas definições: a etiqueta, o álbum, quem envia e quanto, os limites, a ligação de convite e o botão de pausa. O que não se pode desfazer fica à parte no fim de um serviço aberto, numa caixa tracejada que diz o que faz: **Sair** (para um serviço para onde envia) e **Excluir serviço** (para um que oferece). Cada pergunta é feita numa caixa de diálogo da própria página, com uma frase clara e um botão com o nome do que faz (vermelho quando não se pode desfazer); Esc ou Cancelar não mudam nada.

![O separador Atividade: uma cronologia do que aconteceu, a última execução e Sincronizar agora](../../images/activity.png)

## Faça-a sua

- **Ctrl K** (ou **Cmd K** num Mac, ou a lupa na barra, ou a barra de pesquisa da Visão geral) abre a **paleta de comandos**: escreva umas letras para saltar para uma vista ou um serviço, ou para fazer o que pode fazer (aprovar um pedido, pausar ou retomar, sincronizar, abrir a ajuda, fazer a visita). Só lista o que lhe é permitido, e carrega no mesmo botão da página, por isso as perguntas continuam a ser feitas.

  ![A paleta de comandos com vistas, um pedido para aprovar e os serviços](../../images/palette.png)

- **Cores.** O ícone da paleta escolhe o **esquema de cores** (**Crepúsculo**, as cores do ícone da aplicação, ou **Immich**, azuis calmos sobre cinzentos quase neutros), o ícone da meia-lua alterna entre claro e escuro, e o ícone do altifalante liga ou desliga o som suave e a breve vibração do telemóvel quando uma foto aterra (o primeiro clique ou toque acorda o som, uma regra dos navegadores). O que escolher fica guardado neste navegador e prevalece sobre o que o administrador definiu (`THEME` e `THEME_MODE`, veja [Definições](../../reference/settings.md)).
- **Idioma.** O código ao lado do globo (en, de, fr, es, it, pt, ru) é o menu de idiomas: as páginas seguem o idioma do seu navegador e este escolhe outro para sempre. Só as páginas são traduzidas, não o registo.
- **Teclas.** **A** e **D** respondem ao pedido que espera (os botões trazem a sua tecla), e **G** e depois **O**, **S**, **A** ou **T** vai para a Visão geral, os Serviços, a Atividade ou as Definições. Calam-se enquanto escreve ou há uma caixa de diálogo aberta.
- **Num telemóvel** pode adicionar a página ao ecrã inicial para que abra como uma aplicação: veja [Operação](../../admin/operations.md).
- **Movimento.** A luz atrás da página desliza e acende-se quando uma foto aterra, os cartões inclinam-se para um rato e os números rodam como num painel de partidas. Tudo isto é omitido num ecrã tátil ou com movimento reduzido, e nada se mexe enquanto a página está parada.
- **Álbuns como pastas** (um nome como `Viagens / 2024 / Roma`) têm uma página própria: [Árvore de álbuns](../../guides/show/album-tree.md).
- O rodapé diz que **versão** está em execução, às pessoas com sessão iniciada no Immich.

## As palavras usadas aqui

| Palavra | Significado |
|---|---|
| **Serviço** | Um lugar com nome onde se podem entregar fotos, com uma etiqueta. «Férias 2026» com a etiqueta `share` é um serviço. |
| **Destinatário** (ou **proprietário**) | A pessoa na biblioteca de quem as fotos aterram. Cria o serviço e decide quem pode enviar. |
| **Remetente** | Alguém que marca fotos para as entregar a um serviço. |
| **Etiqueta** | A marca que acrescenta a uma foto no Immich para a enviar. Cada serviço tem a sua. |
| **Caixa de entrada** | Uma etiqueta em tudo o que acabou de chegar, para o destinatário encontrar o que é novo. |
| **Caixa de entrega** | Uma ligação, código QR ou endereço de e-mail para onde qualquer pessoa pode enviar fotos sem conta. Pode também ser um **evento** com galeria, mural e uma seleção do melhor. |
| **Pool** | Um serviço onde todos enviam e todos recebem, para uma viagem ou uma festa. |
| **Sala de cliente** | Uma ligação para um cliente ver um álbum através de imagens marcadas e escolher favoritas. |
| **Regra** | Uma pesquisa no seu Immich (pessoas, locais, datas, uma frase) que sugere fotos para entregar. |
| **Álbum partilhado** | Um álbum que duas pessoas em servidores diferentes mantêm sincronizado. |
| **Chave** | A chave de API restrita que a aplicação cria na sua própria conta Immich quando se junta, para poder marcar e entregar por si. Ninguém a vê, e apagá-la no Immich pára tudo em segurança. |

## Bom saber

- **Nada é removido sem uma cópia.** Uma foto só vai para o lixo depois de cada destinatário ter uma cópia verificada, e nunca enquanto uma ligação estiver interrompida.
- **Uma foto que o destinatário já tem no lixo não é entregue.** O original é mantido e recebe a etiqueta `share-duplicate` (a do serviço é substituída), para ver porquê. Quando o destinatário esvaziar o lixo, volte a adicionar a etiqueta.
- **Cópias de segurança do telemóvel.** Uma foto no seu lixo é ignorada pela cópia de segurança do telemóvel. Quando o Immich esvazia o lixo, um telemóvel que ainda tenha a foto pode voltar a guardá-la.
- **Uma etiqueta que acabou de adicionar desapareceu.** O Immich por vezes perde uma etiqueta adicionada nos primeiros instantes depois de carregar uma foto. Adicione-a de novo.

Algo não funciona? Veja [Resolução de problemas](../../reference/troubleshooting.md).

## Quando a etiqueta da caixa de entrada desaparece

- **Arrumada num álbum:** a etiqueta é removida em cerca de 15 minutos, não de imediato.
- **Em nenhum álbum:** a etiqueta é removida quando a foto esteve na biblioteca do destinatário durante o prazo do serviço (30 dias por predefinição). O relógio começa quando a foto foi carregada no Immich pela entrega, não quando foi tirada, e editar ou voltar a marcar a foto não o repõe.

**Porquê um atraso?** A aplicação sabe das fotos novas porque o Immich lho diz: um workflow dispara quando se adiciona uma etiqueta e chama a aplicação. O Immich 3.2.0 não oferece um evento assim para os álbuns (os únicos disparadores são a criação de um item, a extração dos seus metadados e a adição de uma etiqueta), por isso a única maneira de dar por isso é ir ver. A aplicação verifica com um temporizador, e a verificação é propositadamente pequena: uma pesquisa das fotos com a etiqueta da caixa de entrada por destinatário, e mais nada quando está vazia.

**Mais depressa ou mais devagar?** Altere `CLEANUP_INTERVAL_MINUTES` ([Definições](../../reference/settings.md)). Mais curto significa mais pedidos (pequenos) ao Immich e menos espera; `0` desliga o temporizador e deixa a verificação diária. Se o Immich acrescentar um evento de álbum numa versão futura, isto poderá passar a ser instantâneo.

## O que se segue

- Entregar a sua primeira foto: [Início rápido](quickstart.md)
- Escolher um objetivo: [Utilização](../using.md)
- Procurar algo: [As perguntas que se fazem primeiro](../reference/faq.md)
