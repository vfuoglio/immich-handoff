> [!NOTE]
> Esta página foi traduzida automaticamente e ainda não foi revista por uma pessoa. Os guias ligados continuam em inglês enquanto não forem traduzidos.

# As perguntas que se fazem primeiro

Respostas curtas, e onde ler mais.

## O essencial

**É a mesma coisa que os álbuns partilhados ou as ligações de partilha do Immich?**
Não. Esses deixam as pessoas *ver* as suas fotos. O Handoff põe uma foto **noutra biblioteca**, recolhe fotos de pessoas sem conta e liga dois servidores. Veja a [comparação](../../start/why-handoff.md).

**A foto passa mesmo para a biblioteca da outra pessoa?**
Sim. Chega uma cópia ao Immich dela como foto sua, com o nome, as datas, a marca de favorita, a descrição, a classificação e o local. [O que é transferido](../../reference/what-is-carried-over.md) lista o que viaja e o que não.

**De que preciso?**
Immich 3.2.0 ou posterior, Docker e as etiquetas ativadas para todas as pessoas envolvidas. Um pequeno contentor, sem servidor de base de dados. Veja [Instalação](../../admin/install.md).

**Quem envia tem de instalar alguma coisa?**
Não. Usam o Immich como antes: adicionar uma etiqueta, ou carregar no botão **Entregar…** se o seu administrador o [adicionou](../../admin/find-handoff.md). Os convidados sem conta usam uma ligação ou um código QR no navegador.

**Funciona com a aplicação móvel do Immich?**
Marcar na aplicação funciona. O botão dentro do Immich funciona num navegador, não na aplicação nativa. A página também envia fotos a partir de um telemóvel: veja [Enviar fotos a partir do telemóvel](../../guides/collect/phone.md).

## As suas fotos e as suas chaves

**O que acontece ao meu original?**
Escolhe ao juntar-se a um serviço: **mantê-lo** (é enviada uma cópia) ou **movê-lo** para o seu lixo, o que só acontece quando o outro lado tem uma cópia verificada. O destinatário não pode decidir isso por si.

**Tenho de dar a alguém a minha palavra-passe ou uma chave?**
Não. A aplicação cria uma chave com nome e restrita na *sua* conta com o seu próprio início de sessão, guarda-a cifrada e ninguém a vê. Apague-a no Immich, ou carregue em **Sair**, e tudo pára sem tocar numa foto. Veja [Segurança](../../reference/security.md).

**É oficial?**
Não. É um projeto comunitário independente, não feito nem aprovado pela equipa do Immich.

## O que mais consegue fazer

**Recolher fotos de pessoas sem conta Immich, para um casamento ou uma festa?**
Sim. Uma [caixa de entrega](../../guides/collect/events.md) é uma ligação ou um código QR só de escrita: aprove primeiro as fotos, mostre-as num mural, dê aos convidados uma galeria com corações, faça uma seleção do melhor.

**Entregar uma sessão a clientes?**
Sim. Uma [sala de cliente](../../guides/collect/client-rooms.md) mostra um álbum através de imagens marcadas, o cliente escolhe favoritas e você liberta os originais quando decidir.

**Partilhar com alguém noutro servidor Immich?**
Sim, com os [álbuns partilhados entre servidores](../../guides/share/shared-albums.md). O seu amigo nem precisa do Handoff se lhe der uma chave com uma curta lista de permissões.

**Partilhar as fotos de uma pessoa com o meu parceiro, nos dois sentidos?**
Sim. Nas **Definições**, **Propor partilhar fotos** cria uma partilha privada entre duas pessoas do servidor: cada uma escolhe as pessoas cujas fotos vão para a outra, a outra diz que sim numa página própria, e essas fotos são propostas com imagens (ou enviadas sozinhas, se o ativar). Não é instantâneo, cada biblioteca guarda a sua própria cópia e ainda não reconhece animais. Veja [Partilhar as fotos de uma pessoa](../../guides/share/share-people.md).

**Guardar uma foto que alguém partilhou comigo?**
Sim. O botão dentro do Immich mostra **Adicionar à minha biblioteca** nas fotos dos álbuns partilhados consigo e de um parceiro. Veja [Adicionar à minha biblioteca](../../admin/find-handoff.md), ou [siga o álbum](../../guides/share/follow-album.md) para lhe serem propostas as fotos novas à medida que são adicionadas.

**Enviar fotos para um NAS, Nextcloud, S3 ou SFTP, em vez de ou além de outra conta Immich?**
Sim. O administrador nomeia os locais num ficheiro e o proprietário de um serviço adiciona um. A foto é escrita, verificada, e só depois o original é movido ou marcado; se um local estiver indisponível, o original continua marcado até o ter. Veja [Destinos](../../guides/share/destinations.md).

**Empilhar os meus ficheiros RAW e JPEG, edições e rajadas?**
Sim, com o seu aval: **Empilhar as fotos que andam juntas** propõe cada pilha, nunca altera nem apaga uma foto e pode desfazer as suas próprias pilhas durante 15 minutos. Veja [Empilhar](../../guides/automate/stacking.md).

**Fazer álbuns a partir das pastas da minha biblioteca externa?**
Sim: **Álbuns das minhas pastas** propõe um álbum por pasta e põe lá as fotos quando diz que sim. Só acrescenta. Veja [Álbuns de pastas](../../guides/automate/folder-albums.md).

**Dar às fotos de uma câmara sem GPS o local onde foram tiradas?**
Sim, com cuidado: **Locais para fotos que não têm** propõe o local de uma foto tirada minutos antes ou depois pela mesma câmara, e não escreve nada até dizer que sim. A aplicação não consegue desfazer um local escrito (o Immich não o permite). Veja [Locais](../../guides/automate/places.md).

**Fazer um resumo para partilhar de uma viagem ou de um ano?**
Sim: as [Histórias](../../guides/show/stories.md) transformam um álbum, uma pessoa, um período ou uma caixa de entrega num breve resumo animado com uma ligação que qualquer pessoa pode abrir sem conta.

**Mostrar fotos na minha TV com um código?**
Sim: abra `/tv` no navegador da TV e escreva o seu código em **Mostrar numa TV**. Veja [Mostrar numa TV](../../guides/show/cast-to-tv.md).

**Ver os meus álbuns como pastas, como Viagens / 2024 / Roma?**
Sim, numa página à parte: dê aos álbuns nomes com barras e abra **Árvore de álbuns**. Veja [Árvore de álbuns](../../guides/show/album-tree.md).

**Receber uma mensagem nos dias em que o Immich tem memórias para mim?**
Sim: [Neste dia](../../guides/show/memories.md) envia uma mensagem por dia, por notificação ou e-mail, quando há memórias que ainda não viu.

## O futuro

**E se o Immich mudar?**
Os testes do projeto correm contra um Immich real, e o botão dentro do Immich é verificado contra as suas páginas na versão fixada, por isso uma mudança aparece quando essa versão é subida. Se o botão não encontrar o seu lugar, esconde-se sozinho e nada mais se parte.

**E se o Immich o acrescentar por si?**
Pode acrescentar uma parte. O Handoff só usa a API pública do Immich e não altera nada lá dentro; se o Immich cobrir uma parte do que usa, pode simplesmente deixar de usar essa parte e nada mais é afetado. O que continuaria a ser útil por si: escolher se o original fica ou é movido (e que nada seja removido antes de existir uma cópia verificada), convidados sem conta que enviam por um código QR com moderação, um mural e uma galeria, salas de cliente para fotógrafos e a partilha entre dois servidores. Sair é fácil: apague a chave no Immich, ou carregue em **Sair**, e cada foto fica onde está.

## O que se segue

- Entregar uma primeira foto: [Início rápido](../start/quickstart.md)
- Escolher um objetivo: [Utilização](../using.md)
- Algo não funciona: [Resolução de problemas](../../reference/troubleshooting.md)
