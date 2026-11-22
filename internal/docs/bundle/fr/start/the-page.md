> [!NOTE]
> Cette page a été traduite automatiquement et n’a pas encore été relue par une personne. Les images montrent l’interface en anglais. En cas de doute, la version anglaise fait foi.

# S’orienter sur la page

La page a quatre vues et une barre de recherche qui fait ce que vous demandez. Voici la visite de cinq minutes : où sont les choses, ce que veulent dire les mots et ce que montre la scène en direct.

![L’Aperçu : ce qui vous attend, la scène en direct et les chiffres de la semaine](../../images/dashboard-owner.png)

## D’abord : les étiquettes

Handoff fonctionne avec les **étiquettes** d’Immich, et les étiquettes sont **désactivées par défaut**, pour chaque personne séparément. Si les vôtres sont désactivées, la page affiche un avis avec un bouton **Activer les étiquettes** (vous pouvez aussi le faire dans Immich : Réglages du compte, Fonctionnalités, **Étiquettes**). Les expéditeurs en ont besoin pour étiqueter des photos, les destinataires pour voir l’étiquette de la boîte de réception et filtrer dessus. Les activer ne suffit pas : **actualisez l’onglet ou la fenêtre Immich ensuite**, sinon les options d’étiquette n’apparaissent pas (sur l’application mobile, fermez-la et rouvrez-la).

## Les quatre vues

La barre du haut a quatre vues, dans une pilule en son milieu (sur un téléphone, c’est une barre en bas de l’écran, avec une icône chacune). L’adresse retient la vue (`#services`), vous pouvez donc la mettre en favori, et la page y revient après une action.

- **Aperçu.** La date et un message d’accueil, la barre de recherche, puis **À traiter**, la **scène en direct** et **Cette semaine**, l’un sous l’autre. La carte **Pour commencer** se trouve à la fin, jusqu’à ce que vous en ayez fini avec elle.
- **Services.** Chaque service auquel vous envoyez, dont vous recevez, que vous proposez ou que vous pouvez rejoindre. Si vous pouvez créer plus que des services, un sélecteur en haut passe de vos services à vos boîtes de dépôt, salles client, cadres et albums partagés.
- **Activité.** Ce qui s’est passé, la dernière exécution, et **Synchroniser maintenant**.
- **Réglages.** Ce que vous configurez une fois puis oubliez, par catégories (Notifications, Photos, Partage actif, Écrans). La vue n’existe que si quelque chose y est activé pour vous.

### À traiter

Tout ce qui attend une personne vient en premier, le plus urgent d’abord, chaque élément avec son bouton. Quand rien n’attend, c’est dit.

- **Connexion perdue** et **Supprimé par son propriétaire** : **Nettoyer**, qui supprime ce que l’application a créé dans votre compte Immich (vous pouvez ensuite rejoindre à nouveau).
- **Déconnecté** (un service que vous proposez) : **Reconnecter**.
- **Demande** : quelqu’un veut envoyer à un service que vous proposez. **Approuver** ou **Refuser** ; une demande se traite ici et nulle part ailleurs.
- **En pause** : un service que vous avez mis en pause, ou vos propres envois (**Reprendre**), ou un service que son propriétaire a mis en pause (rien à faire de votre côté).
- **Limite** : un expéditeur a atteint la limite quotidienne, ou vous (voir [les limites quotidiennes](../../guides/automate/controls.md)).

### Cette semaine

Un petit graphique avec un point par jour (jours UTC, aujourd’hui à droite) du nombre de photos transmises ces sept derniers jours : les photos que **vous avez envoyées** et celles **arrivées dans les services que vous proposez**. Il compte ce que l’application a enregistré pour les personnes qui ont rejoint depuis la page ; les expéditeurs configurés dans `.env` ou un fichier de routes n’y figurent donc pas. À côté, **En attente de transmission** (les photos que vous avez étiquetées et qui doivent encore partir) et **Dans votre boîte de réception** (ce qui est arrivé et pas encore rangé) se mettent à jour d’eux-mêmes dès qu’une transmission se termine.

### Pour commencer

La carte active les étiquettes, vous aide à rejoindre ou proposer un service et à transmettre une première photo, et suggère d’autres étapes adaptées à ce que vous faites (inviter quelqu’un, une notification). Chaque étape a **Montre-moi** et un **?** vers son guide, et un anneau montre où vous en êtes. Elle disparaît quand vous avez fini ou quand vous appuyez sur **Masquer ce guide**.

## La scène en direct

![La scène en direct : une voie pour chaque expéditeur et une porte vers la bibliothèque](../../images/live-scene.png)

Juste sous la barre de recherche, **En direct** montre ce qui se passe au moment où cela se passe. Chaque service auquel vous participez a une scène : une **voie** pour chaque expéditeur et une porte vers la bibliothèque qui reçoit. Un carré lumineux coloré parcourt la voie de l’expéditeur jusqu’à la porte et un anneau s’étend là où il atterrit ; quand c’est vous qui envoyez, un carré contouré avec une flèche quitte plutôt votre porte. Le compteur **arrivé pendant que vous regardez** monte pour chacun. Les carrés ne sont que des carrés : la scène ne montre jamais de photo ni de nom de fichier, et seulement de vraies transmissions, jamais des inventées.

Une voie dit aussi en mots dans quel état elle est, pour que la couleur ne soit jamais le seul signe : **En cours**, **Suspendu par le propriétaire** ou **Suspendu par l’expéditeur** (voie en pointillés), **Limite quotidienne atteinte** avec le nombre en attente et jusqu’à quand (ambre), **Connexion perdue** (rouge, avec une croix). Un court journal sous les scènes liste les dernières choses en une phrase chacune, et un lecteur d’écran reçoit la même phrase.

Elle reste calme : rien ne bouge tant que rien ne se passe, seuls quelques carrés volent à la fois, et avec le mouvement réduit ou un onglet masqué, les carrés sont omis et seuls les libellés, le compteur et le journal changent. Si la connexion à l’application tombe, la scène s’assombrit et le dit, puis rattrape d’elle-même.

![L’onglet Services : un service auquel vous envoyez, ouvert sur place, et un que vous pouvez rejoindre](../../images/services-sender.png)

Chaque service tient sur une ligne : son initiale, son nom, ce qu’il est, un état éventuel comme En pause ou Limite, et le chiffre qui compte (photos en attente, ou dans votre boîte de réception). Appuyez dessus pour l’ouvrir sur place et voir ses réglages : l’étiquette, l’album, qui envoie et combien, les limites, le lien d’invitation et le bouton de pause. Ce qui ne peut pas être annulé se trouve à part, en bas d’un service ouvert, dans un cadre en pointillés qui dit ce qu’il fait : **Quitter** (pour un service auquel vous envoyez) et **Supprimer le service** (pour un service que vous proposez). Chaque question est posée dans une boîte de dialogue de la page elle-même, avec une phrase claire et un bouton nommé d’après ce qu’il fait (rouge quand c’est irréversible) ; Échap ou Annuler ne change rien.

![L’onglet Activité : une chronologie de ce qui s’est passé, la dernière exécution et Synchroniser maintenant](../../images/activity.png)

## Faites-la vôtre

- **Ctrl K** (ou **Cmd K** sur un Mac, ou la loupe dans la barre, ou la barre de recherche de l’Aperçu) ouvre la **palette de commandes** : tapez quelques lettres pour aller à une vue ou un service, ou pour faire ce que vous pouvez faire (approuver une demande, mettre en pause ou reprendre, synchroniser, ouvrir l’aide, faire la visite). Elle ne liste que ce que vous avez le droit de faire, et appuie sur le même bouton que la page, les questions sont donc toujours posées.

  ![La palette de commandes avec des vues, une demande à approuver et les services](../../images/palette.png)

- **Couleurs.** L’icône de palette choisit la **palette de couleurs** (**Crépuscule**, les couleurs de l’icône de l’application, ou **Immich**, des bleus calmes sur des gris presque neutres), l’icône demi-lune bascule entre clair et sombre, et l’icône haut-parleur active ou coupe le son doux et la courte vibration du téléphone quand une photo atterrit (le premier clic ou la première touche réveille le son, une règle des navigateurs). Ce que vous choisissez est conservé dans ce navigateur et l’emporte sur ce que l’administrateur a défini (`THEME` et `THEME_MODE`, voir [Réglages](../../reference/settings.md)).
- **Langue.** Le code à côté du globe (en, de, fr, es, it, pt, ru) est le menu des langues : les pages suivent la langue de votre navigateur et celui-ci en choisit une autre pour de bon. Seules les pages sont traduites, pas le journal.
- **Touches.** **A** et **D** répondent à la demande en attente (les boutons portent leur touche), et **G** puis **O**, **S**, **A** ou **T** va à l’Aperçu, aux Services, à l’Activité ou aux Réglages. Elles se taisent pendant que vous tapez ou qu’une boîte de dialogue est ouverte.
- **Sur un téléphone**, vous pouvez ajouter la page à l’écran d’accueil pour qu’elle s’ouvre comme une application : voir [Exploitation](../../admin/operations.md).
- **Mouvement.** La lumière derrière la page dérive et s’embrase quand une photo atterrit, les cartes s’inclinent vers une souris et les chiffres défilent comme sur un tableau des départs. Tout cela est omis sur un écran tactile ou avec le mouvement réduit, et rien ne bouge tant que la page est inactive.
- **Les albums comme des dossiers** (un nom comme `Voyages / 2024 / Rome`) ont une page à eux : [Arbre des albums](../../guides/show/album-tree.md).
- Le pied de page indique quelle **version** tourne, aux personnes connectées à Immich.

## Les mots employés ici

| Mot | Sens |
|---|---|
| **Service** | Un lieu nommé où des photos peuvent être transmises, avec une étiquette. « Vacances 2026 » avec l’étiquette `share` est un service. |
| **Destinataire** (ou **propriétaire**) | La personne dans la bibliothèque de laquelle les photos arrivent. Elle crée le service et décide qui peut envoyer. |
| **Expéditeur** | Quelqu’un qui étiquette des photos pour les transmettre à un service. |
| **Étiquette** | Le repère que vous ajoutez à une photo dans Immich pour l’envoyer. Chaque service a la sienne. |
| **Boîte de réception** | Une étiquette sur tout ce qui vient d’arriver, pour que le destinataire trouve ce qui est nouveau. |
| **Boîte de dépôt** | Un lien, un code QR ou une adresse e-mail vers lesquels chacun peut envoyer des photos sans compte. Elle peut aussi être un **événement** avec une galerie, un mur et un best-of. |
| **Pool** | Un service où tout le monde envoie et tout le monde reçoit, pour un voyage ou une fête. |
| **Salle client** | Un lien pour qu’un client regarde un album à travers des images marquées et choisisse ses favoris. |
| **Règle** | Une recherche dans votre Immich (personnes, lieux, dates, une expression) qui suggère des photos à transmettre. |
| **Album partagé** | Un album que deux personnes sur des serveurs différents gardent synchronisé. |
| **Clé** | La clé d’API restreinte que l’application crée dans votre propre compte Immich quand vous rejoignez, pour pouvoir étiqueter et transmettre à votre place. Personne ne la voit, et la supprimer dans Immich arrête tout en toute sécurité. |

## Bon à savoir

- **Rien n’est supprimé sans copie.** Une photo ne va à la corbeille qu’une fois que chaque destinataire a une copie vérifiée, et jamais pendant qu’une connexion est interrompue.
- **Une photo que le destinataire a déjà dans sa corbeille n’est pas transmise.** L’original est conservé et reçoit l’étiquette `share-duplicate` (celle du service est remplacée), pour que vous voyiez pourquoi. Une fois que le destinataire a vidé sa corbeille, ajoutez de nouveau l’étiquette.
- **Sauvegardes du téléphone.** Une photo dans votre corbeille est ignorée par la sauvegarde de votre téléphone. Une fois qu’Immich a vidé la corbeille, un téléphone qui a encore la photo peut la sauvegarder de nouveau.
- **Une étiquette que vous venez d’ajouter a disparu.** Immich perd parfois une étiquette ajoutée dans les premiers instants après l’envoi d’une photo. Ajoutez-la de nouveau.

Quelque chose ne marche pas ? Voir [Dépannage](../../reference/troubleshooting.md).

## Quand l’étiquette de la boîte de réception disparaît

- **Rangée dans un album :** l’étiquette est retirée en une quinzaine de minutes environ, pas instantanément.
- **Dans aucun album :** l’étiquette est retirée une fois que la photo est restée dans la bibliothèque du destinataire pendant la durée d’expiration du service (30 jours par défaut). L’horloge démarre quand la photo a été envoyée dans Immich par la transmission, pas quand elle a été prise, et modifier ou ré-étiqueter la photo ne la remet pas à zéro.

**Pourquoi un délai ?** L’application apprend l’existence des nouvelles photos parce qu’Immich le lui dit : un workflow se déclenche quand une étiquette est ajoutée et appelle l’application. Immich 3.2.0 n’offre pas un tel événement pour les albums (les seuls déclencheurs sont la création d’un élément, l’extraction de ses métadonnées et l’ajout d’une étiquette), donc la seule façon de le remarquer est d’aller voir. L’application vérifie sur un minuteur, et la vérification est volontairement légère : une recherche des photos portant l’étiquette de la boîte de réception par destinataire, et rien de plus quand elle est vide.

**Plus vite ou plus lentement ?** Changez `CLEANUP_INTERVAL_MINUTES` ([Réglages](../../reference/settings.md)). Plus court signifie plus de (petites) requêtes vers Immich et une attente plus courte ; `0` éteint le minuteur et laisse faire la vérification quotidienne. Si Immich ajoute un événement d’album dans une version future, cela pourra devenir instantané.

## Et ensuite

- Transmettre votre première photo : [Démarrage rapide](quickstart.md)
- Choisir un objectif : [Utilisation](../using.md)
- Chercher quelque chose : [Les questions qu’on pose d’abord](../reference/faq.md)
