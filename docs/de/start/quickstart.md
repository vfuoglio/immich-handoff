---
title: "Schnellstart: deine erste Übergabe in zehn Minuten"
tip: "Von null zur ersten Übergabe in zehn Minuten."
source_rev: 0d9741e9
review: needed
---

> [!NOTE]
> Diese Seite wurde maschinell übersetzt und noch nicht von einer Person geprüft. Die Bilder zeigen die englische Oberfläche. Im Zweifel gilt die englische Fassung.

# Schnellstart: deine erste Übergabe in zehn Minuten

Ein Foto, das du in der Immich-App markierst, die du ohnehin benutzt, erscheint in der Bibliothek eines anderen, mit einer geprüften Kopie, bevor dein Original überhaupt angefasst wird.

![Ein Foto markieren, Immich meldet es immich-handoff, das Foto kommt in der Bibliothek des Empfängers an](../../images/flow.svg)

**Was du brauchst:** Immich 3.2.0 oder neuer, Docker auf dem Rechner, auf dem es läuft, und etwa zehn Minuten. Wenn du es erst allein ausprobieren willst, [starte die Demo](../../../demo/README.md): ein Befehl, Beispielfotos, nichts von dir wird angefasst.

## 1. Die App starten

Unter Linux (auf einem Mac `sed -i ''` verwenden):

```
git clone --branch v1.3.1 https://github.com/vfuoglio/immich-handoff.git && cd immich-handoff
cp .env.example .env
sed -i -e "s/^WEBHOOK_SECRET=.*/WEBHOOK_SECRET=$(openssl rand -hex 24)/" -e "s/^HANDOFF_STORE_KEY=.*/HANDOFF_STORE_KEY=$(openssl rand -hex 24)/" .env
docker compose up -d
```

> [!TIP]
> Wenn dein Immich-Netzwerk nicht `immich_default` heißt, setze zuerst `IMMICH_NETWORK` in der `.env`. **Hebe eine Kopie von `HANDOFF_STORE_KEY` auf**: Er verschlüsselt die Schlüssel, die die App für Mitmachende speichert.

## 2. Anmelden und einen Dienst anbieten

Öffne `http://<dein-host>:8080/` unter demselben Hostnamen, den du für Immich benutzt, und melde dich mit deinem Immich-Konto an. Wähle **Einen eigenen Dienst anbieten**, gib ihm einen Namen (zum Beispiel „Urlaub 2026“), wähle das Tag, das Absender benutzen (zum Beispiel `share`), und wer senden darf. Niemand legt von Hand einen API-Schlüssel an: Die App erstellt in jedem Konto in dem Moment einen eingeschränkten, in dem jemand beitritt, und man kann ihn jederzeit in Immich löschen.

![Das Formular zum Anbieten eines Dienstes](../../images/offer-form.png)

## 3. Jemanden beitreten lassen

Teile den Einladungslink oder QR-Code des Dienstes. Die andere Person öffnet ihn, meldet sich mit ihrem eigenen Immich-Konto an und tritt bei. Dabei entscheidet sie selbst, was mit ihren Originalen geschieht: **behalten** und eine Kopie senden, oder in den Papierkorb **verschieben**, sobald deine Seite eine geprüfte Kopie hat.

![Einem Dienst beitreten und wählen, was mit deinen Originalen geschieht](../../images/join-choice.png)

> [!WARNING]
> Wähle **behalten**, solange du ausprobierst. **Verschieben** legt das Original nach der Prüfung der Kopie in den Papierkorb des Absenders.

## 4. Ein Foto markieren

Füge in der Immich-App einem Foto das Tag `share` hinzu. Wenige Sekunden später ist es in der Bibliothek des Empfängers, mit dem Tag `inbox`, und die Seite zeigt es auf dem Weg. Kommt es nicht an, führe `docker compose exec immich-handoff /immich-handoff doctor` aus; der Befehl nennt, was falsch ist und wie man es behebt.

![Die Fotos, wie sie in Immich des Empfängers ankommen](../../images/immich-family.png)

> [!IMPORTANT]
> Alle, die mitmachen, brauchen in Immich eingeschaltete **Tags** (Kontoeinstellungen, Funktionen, Tags) und müssen die Immich-Seite danach neu laden. Die Seite merkt es und bietet eine Schaltfläche **Tags aktivieren** an.

## Wie es weitergeht

- Nicht mehr von Hand markieren: [Regeln](../../guides/automate/rules.md) und [die Schaltfläche in Immich](../../guides/automate/immich-button.md)
- Fotos von Gästen bei einer Veranstaltung sammeln: [Drop-Boxen](../../guides/collect/events.md)
- Vom Handy senden: [der Reiter Senden](../../guides/collect/phone.md)
- Sich auf der Seite zurechtfinden: [die Seite](the-page.md)
- Richtig betreiben: [Installation](../../admin/install.md), dann [Betrieb](../../admin/operations.md)
