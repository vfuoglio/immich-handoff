> [!NOTE]
> Cette page a été traduite automatiquement et n’a pas encore été relue par une personne. Les images montrent l’interface en anglais. En cas de doute, la version anglaise fait foi.

# Démarrage rapide : votre première transmission en dix minutes

Une photo que vous étiquetez dans l’application Immich que vous utilisez déjà apparaît dans la bibliothèque de quelqu’un d’autre, avec une copie vérifiée avant que votre original ne bouge.

![Étiqueter une photo, Immich le signale à immich-handoff, la photo arrive dans la bibliothèque du destinataire](../../images/flow.svg)

**Ce qu’il vous faut :** Immich 3.2.0 ou plus récent, Docker sur la machine qui l’héberge, et une dizaine de minutes. Pour essayer d’abord seul, [lancez la démo](../../../demo/README.md) : une commande, des photos d’exemple, rien de ce qui est à vous n’est touché.

## 1. Démarrer l’application

Sous Linux (sur un Mac, utilisez `sed -i ''`) :

```
git clone --branch v1.3.1 https://github.com/vfuoglio/immich-handoff.git && cd immich-handoff
cp .env.example .env
sed -i -e "s/^WEBHOOK_SECRET=.*/WEBHOOK_SECRET=$(openssl rand -hex 24)/" -e "s/^HANDOFF_STORE_KEY=.*/HANDOFF_STORE_KEY=$(openssl rand -hex 24)/" .env
docker compose up -d
```

> [!TIP]
> Si votre réseau Immich ne s’appelle pas `immich_default`, définissez d’abord `IMMICH_NETWORK` dans `.env`. **Gardez une copie de `HANDOFF_STORE_KEY`** : elle chiffre les clés que l’application conserve pour ceux qui rejoignent.

## 2. Se connecter et proposer un service

Ouvrez `http://<votre-hôte>:8080/` sur le même nom d’hôte que celui d’Immich et connectez-vous avec votre compte Immich. Choisissez **Proposer votre propre service**, donnez-lui un nom (par exemple « Vacances 2026 »), choisissez l’étiquette que les expéditeurs utiliseront (par exemple `share`) et qui peut envoyer. Personne ne crée de clé d’API à la main : l’application en crée une, restreinte, dans chaque compte au moment où une personne rejoint, et elle peut la supprimer dans Immich à tout moment.

![Le formulaire pour proposer un service](../../images/offer-form.png)

## 3. Laisser quelqu’un rejoindre

Partagez le lien d’invitation ou le code QR du service. L’autre personne l’ouvre, se connecte avec son propre compte Immich et rejoint, en choisissant elle-même ce qu’il advient de ses originaux : les **garder** et envoyer une copie, ou les **déplacer** dans sa corbeille une fois que votre côté a une copie vérifiée.

![Rejoindre un service et choisir ce qu’il advient de vos originaux](../../images/join-choice.png)

> [!WARNING]
> Choisissez **garder** tant que vous faites des essais. **Déplacer** met l’original dans la corbeille de l’expéditeur après vérification de la copie.

## 4. Étiqueter une photo

Dans l’application Immich, ajoutez l’étiquette `share` à une photo. Quelques secondes plus tard, elle est dans la bibliothèque du destinataire, avec l’étiquette `inbox`, et la page la montre en route. Si elle n’arrive pas, lancez `docker compose exec immich-handoff /immich-handoff doctor` ; la commande dit ce qui ne va pas et comment le corriger.

![Les photos telles qu’elles arrivent dans l’Immich du destinataire](../../images/immich-family.png)

> [!IMPORTANT]
> Toutes les personnes concernées ont besoin des **étiquettes** activées dans Immich (Réglages du compte, Fonctionnalités, Étiquettes) et doivent recharger la page Immich ensuite. La page s’en aperçoit et propose un bouton **Activer les étiquettes**.

## Et ensuite

- Ne plus étiqueter à la main : [les règles](../../guides/automate/rules.md) et [le bouton dans Immich](../../guides/automate/immich-button.md)
- Collecter les photos d’invités lors d’un événement : [les boîtes de dépôt](../../guides/collect/events.md)
- Envoyer depuis votre téléphone : [l’onglet Envoyer](../../guides/collect/phone.md)
- S’orienter sur la page : [la page](the-page.md)
- L’exploiter correctement : [installation](../../admin/install.md), puis [exploitation](../../admin/operations.md)
