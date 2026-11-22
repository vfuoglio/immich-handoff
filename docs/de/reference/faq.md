---
title: "Fragen, die Leute zuerst stellen"
tip: "Kurze Antworten auf das, was Leute vor dem Start fragen."
source_rev: 9486e154
review: needed
---

> [!NOTE]
> Diese Seite wurde maschinell übersetzt und noch nicht von einer Person geprüft. Die verlinkten Anleitungen sind, soweit nicht übersetzt, noch auf Englisch.

# Fragen, die Leute zuerst stellen

Kurze Antworten und wo du mehr liest.

## Die Grundlagen

**Ist das dasselbe wie Immichs geteilte Alben oder Freigabelinks?**
Nein. Die lassen Leute deine Fotos *ansehen*. Handoff legt ein Foto **in eine andere Bibliothek**, sammelt Fotos von Leuten ohne Konto und verbindet zwei Server. Siehe den [Vergleich](../../start/why-handoff.md).

**Wandert das Foto wirklich in die Bibliothek der anderen Person?**
Ja. Eine Kopie kommt in deren Immich als ihr eigenes Foto an, mit Namen, Daten, Favoritenmarkierung, Beschreibung, Bewertung und Ort. [Was mitübertragen wird](../../reference/what-is-carried-over.md) listet auf, was mitreist und was nicht.

**Was brauche ich?**
Immich 3.2.0 oder neuer, Docker, und eingeschaltete Tags für alle, die mitmachen. Ein kleiner Container, kein Datenbankserver. Siehe [Installation](../../admin/install.md).

**Müssen die, die senden, etwas installieren?**
Nein. Sie benutzen Immich wie bisher: ein Tag hinzufügen oder die Schaltfläche **Übergeben …** drücken, wenn dein Administrator sie [eingebaut hat](../../admin/find-handoff.md). Gäste ohne Konto benutzen einen Link oder QR-Code in ihrem Browser.

**Funktioniert es mit der Immich-Handy-App?**
Das Markieren in der App funktioniert. Die Schaltfläche in Immich funktioniert im Browser, nicht in der nativen App. Die Seite sendet auch Fotos vom Handy: siehe [Fotos vom Handy senden](../../guides/collect/phone.md).

## Deine Fotos und deine Schlüssel

**Was geschieht mit meinem Original?**
Das wählst du beim Beitritt zu einem Dienst: es **behalten** (eine Kopie wird gesendet) oder in den Papierkorb **verschieben**, was erst geschieht, wenn die andere Seite eine geprüfte Kopie hat. Der Empfänger kann das nicht für dich entscheiden.

**Muss ich jemandem mein Passwort oder einen Schlüssel geben?**
Nein. Die App legt in *deinem* Konto mit deiner eigenen Anmeldung einen benannten, eingeschränkten Schlüssel an, speichert ihn verschlüsselt, und niemand sieht ihn. Lösche ihn in Immich oder drücke **Verlassen**, und alles hört auf, ohne ein Foto anzufassen. Siehe [Sicherheit](../../reference/security.md).

**Ist es offiziell?**
Nein. Es ist ein unabhängiges Community-Projekt, nicht vom Immich-Team gemacht oder unterstützt.

## Was es sonst noch kann

**Fotos von Leuten ohne Immich-Konto einsammeln, für eine Hochzeit oder Feier?**
Ja. Eine [Drop-Box](../../guides/collect/events.md) ist ein Link oder QR-Code nur zum Hineinlegen: Fotos vorher genehmigen, auf einer Wand zeigen, Gästen eine Galerie mit Herzen geben, ein Best-of machen.

**Ein Shooting an Kunden liefern?**
Ja. Ein [Kundenraum](../../guides/collect/client-rooms.md) zeigt ein Album durch markierte Bilder, der Kunde wählt Favoriten, und du gibst die Originale frei, wenn du es entscheidest.

**Mit jemandem auf einem anderen Immich-Server teilen?**
Ja, mit [Alben über Server hinweg](../../guides/share/shared-albums.md). Deine Freundin oder dein Freund braucht nicht einmal Handoff, wenn du einen Schlüssel mit einer kurzen Berechtigungsliste gibst.

**Die Fotos einer Person mit meinem Partner teilen, in beide Richtungen?**
Ja. In den **Einstellungen** richtet **Teilen anbieten** ein privates Teilen zwischen zwei Personen auf dem Server ein: Jede wählt die Personen, deren Fotos zur anderen gehen, die andere sagt auf einer eigenen Seite ja, und diese Fotos werden mit Bildern angeboten (oder von selbst gesendet, wenn du das einschaltest). Es ist nicht sofort, jede Bibliothek behält ihre eigene Kopie, und Haustiere erkennt es noch nicht. Siehe [Fotos einer Person teilen](../../guides/share/share-people.md).

**Ein Foto behalten, das jemand mit mir geteilt hat?**
Ja. Die Schaltfläche in Immich zeigt **Zu meiner Bibliothek hinzufügen** bei Fotos aus mit dir geteilten Alben und von einem Partner. Siehe [Zu meiner Bibliothek hinzufügen](../../admin/find-handoff.md), oder [folge dem Album](../../guides/share/follow-album.md), um neue Fotos angeboten zu bekommen, sobald sie hinzukommen.

**Fotos an ein NAS, Nextcloud, S3 oder SFTP senden, statt oder zusätzlich zu einem anderen Immich-Konto?**
Ja. Der Administrator nennt die Orte in einer Datei, und der Besitzer eines Dienstes fügt einen hinzu. Das Foto wird geschrieben, geprüft, und erst dann wird das Original verschoben oder markiert; ist ein Ort nicht erreichbar, bleibt das Original markiert, bis er das Foto hat. Siehe [Ziele](../../guides/share/destinations.md).

**Meine RAW- und JPEG-Dateien, Bearbeitungen und Serien stapeln?**
Ja, mit deiner Zustimmung: **Zusammengehörende Fotos stapeln** bietet jeden Stapel an, ändert oder löscht nie ein Foto und kann seine eigenen Stapel 15 Minuten lang wieder auflösen. Siehe [Stapeln](../../guides/automate/stacking.md).

**Alben aus den Ordnern meiner externen Bibliothek machen?**
Ja: **Alben aus meinen Ordnern** bietet für jeden Ordner ein Album an und legt die Fotos hinein, sobald du ja sagst. Es fügt nur hinzu. Siehe [Alben aus Ordnern](../../guides/automate/folder-albums.md).

**Fotos von einer Kamera ohne GPS den Ort geben, an dem sie entstanden sind?**
Ja, mit Vorsicht: **Orte für Fotos ohne Ort** bietet den Ort eines Fotos an, das Minuten davor oder danach von derselben Kamera aufgenommen wurde, und schreibt nichts, bevor du ja sagst. Die App kann einen geschriebenen Ort nicht zurücknehmen (Immich erlaubt es nicht). Siehe [Orte](../../guides/automate/places.md).

**Einen teilbaren Rückblick auf eine Reise oder ein Jahr machen?**
Ja: [Geschichten](../../guides/show/stories.md) machen aus einem Album, einer Person, einem Zeitraum oder einer Drop-Box einen kurzen animierten Rückblick mit einem Link, den jeder ohne Konto öffnen kann.

**Fotos mit einem Code auf meinem Fernseher zeigen?**
Ja: Öffne `/tv` im Browser des Fernsehers und tippe seinen Code bei **Auf einem Fernseher zeigen** ein. Siehe [Auf einem Fernseher zeigen](../../guides/show/cast-to-tv.md).

**Meine Alben als Ordner sehen, wie Reisen / 2024 / Rom?**
Ja, auf einer eigenen Seite: Benenne Alben mit Schrägstrichen und öffne **Albumbaum**. Siehe [Albumbaum](../../guides/show/album-tree.md).

**Eine Nachricht an den Tagen bekommen, an denen Immich Erinnerungen für mich hat?**
Ja: [An diesem Tag](../../guides/show/memories.md) schickt eine Nachricht pro Tag, per Benachrichtigung oder E-Mail, wenn es Erinnerungen gibt, die du noch nicht angesehen hast.

## Die Zukunft

**Was, wenn sich Immich ändert?**
Die Tests des Projekts laufen gegen ein echtes Immich, und die Schaltfläche in Immich wird auf der festgelegten Version gegen dessen Seiten geprüft, eine Änderung zeigt sich also, wenn diese Version angehoben wird. Findet die Schaltfläche ihren Platz nicht, versteckt sie sich selbst, und nichts anderes geht kaputt.

**Was, wenn Immich das selbst einbaut?**
Es kann einen Teil davon einbauen. Handoff benutzt nur Immichs öffentliche API und ändert nichts darin; deckt Immich also einen Teil dessen ab, wofür du es nutzt, kannst du diesen Teil einfach nicht mehr nutzen, und nichts anderes ist betroffen. Was für sich nützlich bliebe: wählen, ob das Original bleibt oder verschoben wird (und nichts entfernt wird, bevor eine geprüfte Kopie existiert), Gäste ohne Konto, die über einen QR-Code mit Moderation senden, eine Wand und eine Galerie, Kundenräume für Fotografen und das Teilen zwischen zwei Servern. Aufhören ist leicht: Lösche den Schlüssel in Immich oder drücke **Verlassen**, und jedes Foto bleibt, wo es ist.

## Wie es weitergeht

- Ein erstes Foto übergeben: [Schnellstart](../start/quickstart.md)
- Ein Ziel wählen: [Benutzung](../using.md)
- Etwas funktioniert nicht: [Fehlerbehebung](../../reference/troubleshooting.md)
