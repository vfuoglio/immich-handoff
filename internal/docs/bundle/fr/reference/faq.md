> [!NOTE]
> Cette page a été traduite automatiquement et n’a pas encore été relue par une personne. Les guides liés sont encore en anglais tant qu’ils ne sont pas traduits.

# Les questions qu’on pose d’abord

Des réponses courtes, et où en lire davantage.

## L’essentiel

**Est-ce la même chose que les albums partagés ou les liens de partage d’Immich ?**
Non. Ceux-là laissent des gens *regarder* vos photos. Handoff met une photo **dans une autre bibliothèque**, collecte des photos auprès de gens sans compte et relie deux serveurs. Voir la [comparaison](../../../README.md).

**La photo passe-t-elle vraiment dans la bibliothèque de l’autre personne ?**
Oui. Une copie arrive dans son Immich comme sa propre photo, avec son nom, ses dates, sa marque de favori, sa description, sa note et son lieu. [Ce qui est transféré](../../reference/what-is-carried-over.md) liste ce qui voyage et ce qui reste.

**De quoi ai-je besoin ?**
Immich 3.2.0 ou plus récent, Docker, et les étiquettes activées pour toutes les personnes concernées. Un petit conteneur, pas de serveur de base de données. Voir [Installation](../../admin/install.md).

**Ceux qui envoient doivent-ils installer quelque chose ?**
Non. Ils utilisent Immich comme avant : ajouter une étiquette, ou appuyer sur le bouton **Transmettre…** si votre administrateur l’[a ajouté](../../admin/find-handoff.md). Les invités sans compte utilisent un lien ou un code QR dans leur navigateur.

**Cela fonctionne-t-il avec l’application mobile Immich ?**
Étiqueter dans l’application fonctionne. Le bouton dans Immich fonctionne dans un navigateur, pas dans l’application native. La page envoie aussi des photos depuis un téléphone : voir [Envoyer des photos depuis un téléphone](../../guides/collect/phone.md).

## Vos photos et vos clés

**Qu’advient-il de mon original ?**
Vous choisissez en rejoignant un service : le **garder** (une copie est envoyée) ou le **déplacer** dans votre corbeille, ce qui n’arrive qu’une fois que l’autre côté a une copie vérifiée. Le destinataire ne peut pas décider cela à votre place.

**Dois-je donner à quelqu’un mon mot de passe ou une clé ?**
Non. L’application crée une clé nommée et restreinte dans *votre* compte avec votre propre connexion, la conserve chiffrée, et personne ne la voit. Supprimez-la dans Immich, ou appuyez sur **Quitter**, et tout s’arrête sans toucher une photo. Voir [Sécurité](../../reference/security.md).

**Est-ce officiel ?**
Non. C’est un projet communautaire indépendant, ni créé ni approuvé par l’équipe d’Immich.

## Ce qu’il sait faire d’autre

**Collecter des photos auprès de gens sans compte Immich, pour un mariage ou une fête ?**
Oui. Une [boîte de dépôt](../../guides/collect/events.md) est un lien ou un code QR en écriture seule : approuvez les photos d’abord, montrez-les sur un mur, donnez aux invités une galerie avec des cœurs, faites un best-of.

**Livrer une séance à un client ?**
Oui. Une [salle client](../../guides/collect/client-rooms.md) montre un album à travers des images marquées, le client choisit ses favoris, et vous libérez les originaux quand vous décidez.

**Partager avec quelqu’un sur un autre serveur Immich ?**
Oui, avec les [albums partagés entre serveurs](../../guides/share/shared-albums.md). Votre ami n’a même pas besoin de Handoff si vous lui donnez une clé avec une courte liste d’autorisations.

**Partager les photos d’une personne avec mon partenaire, dans les deux sens ?**
Oui. Dans les **Réglages**, **Proposer de partager des photos** met en place un partage privé entre deux personnes du serveur : chacune choisit les personnes dont les photos vont vers l’autre, l’autre dit oui sur une page à elle, et ces photos sont proposées avec des images (ou envoyées d’elles-mêmes, si vous l’activez). Ce n’est pas instantané, chaque bibliothèque garde sa propre copie, et il ne reconnaît pas encore les animaux. Voir [Partager les photos d’une personne](../../guides/share/share-people.md).

**Garder une photo que quelqu’un a partagée avec moi ?**
Oui. Le bouton dans Immich affiche **Ajouter à ma bibliothèque** sur les photos des albums partagés avec vous et d’un partenaire. Voir [Ajouter à ma bibliothèque](../../admin/find-handoff.md), ou [suivez l’album](../../guides/share/follow-album.md) pour qu’on vous propose les nouvelles photos au fur et à mesure.

**Envoyer des photos vers un NAS, Nextcloud, S3 ou SFTP, à la place ou en plus d’un autre compte Immich ?**
Oui. L’administrateur nomme les lieux dans un fichier et le propriétaire d’un service en ajoute un. La photo est écrite, vérifiée, et ce n’est qu’ensuite que l’original est déplacé ou marqué ; si un lieu est indisponible, l’original reste étiqueté jusqu’à ce qu’il ait la photo. Voir [Destinations](../../guides/share/destinations.md).

**Empiler mes fichiers RAW et JPEG, mes retouches et mes rafales ?**
Oui, avec votre accord : **Empiler les photos qui vont ensemble** propose chaque pile, ne modifie ni ne supprime jamais une photo et peut défaire ses propres piles pendant 15 minutes. Voir [Empilement](../../guides/automate/stacking.md).

**Faire des albums à partir des dossiers de ma bibliothèque externe ?**
Oui : **Albums de mes dossiers** propose un album pour chaque dossier et y met les photos une fois que vous dites oui. Il ne fait qu’ajouter. Voir [Albums à partir de dossiers](../../guides/automate/folder-albums.md).

**Donner aux photos d’un appareil sans GPS le lieu où elles ont été prises ?**
Oui, avec prudence : **Lieux pour les photos qui n'en ont pas** propose le lieu d’une photo prise quelques minutes avant ou après par le même appareil, et n’écrit rien avant que vous disiez oui. L’application ne peut pas reprendre un lieu écrit (Immich ne le permet pas). Voir [Lieux](../../guides/automate/places.md).

**Faire un récit partageable d’un voyage ou d’une année ?**
Oui : les [Histoires](../../guides/show/stories.md) font d’un album, d’une personne, d’une période ou d’une boîte de dépôt un court récit animé avec un lien que chacun peut ouvrir sans compte.

**Montrer des photos sur ma télé avec un code ?**
Oui : ouvrez `/tv` dans le navigateur de la télé et tapez son code dans **Diffuser sur une télé**. Voir [Diffuser sur une télé](../../guides/show/cast-to-tv.md).

**Voir mes albums comme des dossiers, comme Voyages / 2024 / Rome ?**
Oui, sur une page à part : nommez les albums avec des barres obliques et ouvrez **Arbre des albums**. Voir [Arbre des albums](../../guides/show/album-tree.md).

**Recevoir un message les jours où Immich a des souvenirs pour moi ?**
Oui : [Ce jour-là](../../guides/show/memories.md) envoie un message par jour, par notification ou par e-mail, quand il y a des souvenirs que vous n’avez pas encore regardés.

## L’avenir

**Et si Immich change ?**
Les tests du projet tournent contre un vrai Immich, et le bouton dans Immich est vérifié contre ses pages sur la version fixée, un changement apparaît donc quand cette version est relevée. Si le bouton ne trouve pas sa place, il se cache de lui-même et rien d’autre ne casse.

**Et si Immich intègre cela lui-même ?**
Il pourrait en intégrer une partie. Handoff n’utilise que l’API publique d’Immich et ne change rien à l’intérieur ; si Immich couvre une partie de ce que vous en faites, vous pouvez simplement cesser d’utiliser cette partie et rien d’autre n’est affecté. Ce qui resterait utile en soi : choisir si l’original reste ou est déplacé (et que rien n’est retiré avant qu’une copie vérifiée existe), des invités sans compte qui envoient via un code QR avec modération, un mur et une galerie, des salles client pour les photographes, et le partage entre deux serveurs. Partir est facile : supprimez la clé dans Immich, ou appuyez sur **Quitter**, et chaque photo reste où elle est.

## Et ensuite

- Transmettre une première photo : [Démarrage rapide](../start/quickstart.md)
- Choisir un objectif : [Utilisation](../using.md)
- Quelque chose ne marche pas : [Dépannage](../../reference/troubleshooting.md)
