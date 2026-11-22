---
title: "Le domande che si fanno per prime"
tip: "Risposte brevi a ciò che si chiede prima di iniziare."
source_rev: 9486e154
review: needed
---

> [!NOTE]
> Questa pagina è stata tradotta automaticamente e non è ancora stata rivista da una persona. Le guide collegate sono ancora in inglese finché non sono tradotte.

# Le domande che si fanno per prime

Risposte brevi, e dove leggere di più.

## Le basi

**È la stessa cosa degli album condivisi o dei link di condivisione di Immich?**
No. Quelli lasciano che le persone *guardino* le tue foto. Handoff mette una foto **in un’altra libreria**, raccoglie foto da persone senza account e collega due server. Vedi il [confronto](../../start/why-handoff.md).

**La foto passa davvero nella libreria dell’altra persona?**
Sì. Una copia arriva nel suo Immich come foto sua, con nome, date, segno di preferita, descrizione, valutazione e luogo. [Cosa viene trasferito](../../reference/what-is-carried-over.md) elenca cosa viaggia e cosa no.

**Di cosa ho bisogno?**
Immich 3.2.0 o successivo, Docker e i tag attivati per tutte le persone coinvolte. Un piccolo container, nessun server di database. Vedi [Installazione](../../admin/install.md).

**Chi invia deve installare qualcosa?**
No. Usano Immich come prima: aggiungere un tag, o premere il pulsante **Consegna…** se il tuo amministratore lo [ha aggiunto](../../admin/find-handoff.md). Gli ospiti senza account usano un link o un codice QR nel loro browser.

**Funziona con l’app mobile di Immich?**
Contrassegnare nell’app funziona. Il pulsante dentro Immich funziona in un browser, non nell’app nativa. La pagina invia anche foto dal telefono: vedi [Inviare foto dal telefono](../../guides/collect/phone.md).

## Le tue foto e le tue chiavi

**Cosa succede al mio originale?**
Lo scegli quando ti unisci a un servizio: **tenerlo** (viene inviata una copia) o **spostarlo** nel tuo cestino, cosa che accade solo quando l’altro lato ha una copia verificata. Il destinatario non può deciderlo per te.

**Devo dare a qualcuno la mia password o una chiave?**
No. L’app crea una chiave con nome e limitata nel *tuo* account con il tuo accesso, la conserva cifrata e nessuno la vede. Eliminala in Immich, o premi **Esci**, e tutto si ferma senza toccare una foto. Vedi [Sicurezza](../../reference/security.md).

**È ufficiale?**
No. È un progetto comunitario indipendente, non creato né approvato dal team di Immich.

## Cos’altro può fare

**Raccogliere foto da persone senza account Immich, per un matrimonio o una festa?**
Sì. Una [drop box](../../guides/collect/events.md) è un link o un codice QR solo in scrittura: approva prima le foto, mostrale su un muro, dai agli ospiti una galleria con cuori, fai una selezione del meglio.

**Consegnare un servizio fotografico a un cliente?**
Sì. Una [stanza cliente](../../guides/collect/client-rooms.md) mostra un album attraverso immagini contrassegnate, il cliente sceglie i preferiti e tu rilasci gli originali quando decidi.

**Condividere con qualcuno su un altro server Immich?**
Sì, con gli [album condivisi tra server](../../guides/share/shared-albums.md). Il tuo amico non ha nemmeno bisogno di Handoff se gli dai una chiave con una breve lista di permessi.

**Condividere le foto di una persona con il mio partner, in entrambe le direzioni?**
Sì. Nelle **Impostazioni**, **Proponi di condividere foto** crea una condivisione privata tra due persone del server: ciascuna sceglie le persone le cui foto vanno all’altra, l’altra dice sì su una pagina propria, e quelle foto vengono proposte con le immagini (o inviate da sole, se lo attivi). Non è istantaneo, ogni libreria tiene la propria copia, e non riconosce ancora gli animali. Vedi [Condividere le foto di una persona](../../guides/share/share-people.md).

**Tenere una foto che qualcuno ha condiviso con me?**
Sì. Il pulsante dentro Immich mostra **Aggiungi alla mia libreria** sulle foto degli album condivisi con te e di un partner. Vedi [Aggiungi alla mia libreria](../../admin/find-handoff.md), o [segui l’album](../../guides/share/follow-album.md) per vederti proporre le nuove foto man mano che vengono aggiunte.

**Inviare foto a un NAS, Nextcloud, S3 o SFTP, invece di o oltre a un altro account Immich?**
Sì. L’amministratore indica i luoghi in un file e il proprietario di un servizio ne aggiunge uno. La foto viene scritta, verificata, e solo dopo l’originale viene spostato o contrassegnato; se un luogo non risponde, l’originale resta contrassegnato finché non ha la foto. Vedi [Destinazioni](../../guides/share/destinations.md).

**Impilare i miei file RAW e JPEG, le modifiche e le raffiche?**
Sì, con il tuo consenso: **Impila le foto che vanno insieme** propone ogni pila, non modifica né elimina mai una foto e può disfare le proprie pile per 15 minuti. Vedi [Impilare](../../guides/automate/stacking.md).

**Fare album dalle cartelle della mia libreria esterna?**
Sì: **Album dalle mie cartelle** propone un album per ogni cartella e vi mette le foto quando dici sì. Aggiunge soltanto. Vedi [Album dalle cartelle](../../guides/automate/folder-albums.md).

**Dare alle foto di una fotocamera senza GPS il luogo in cui sono state scattate?**
Sì, con cautela: **Luoghi per le foto che non ne hanno** propone il luogo di una foto scattata minuti prima o dopo dalla stessa fotocamera, e non scrive nulla finché non dici sì. L’app non può annullare un luogo scritto (Immich non lo permette). Vedi [Luoghi](../../guides/automate/places.md).

**Fare un riepilogo condivisibile di un viaggio o di un anno?**
Sì: le [Storie](../../guides/show/stories.md) trasformano un album, una persona, un periodo o una drop box in un breve riepilogo animato con un link che chiunque può aprire senza account.

**Mostrare foto sulla mia TV con un codice?**
Sì: apri `/tv` nel browser della TV e scrivi il suo codice in **Trasmetti su una TV**. Vedi [Trasmettere su una TV](../../guides/show/cast-to-tv.md).

**Vedere i miei album come cartelle, come Viaggi / 2024 / Roma?**
Sì, su una pagina a parte: dai agli album nomi con barre e apri **Albero degli album**. Vedi [Albero degli album](../../guides/show/album-tree.md).

**Ricevere un messaggio nei giorni in cui Immich ha ricordi per me?**
Sì: [In questo giorno](../../guides/show/memories.md) invia un messaggio al giorno, per notifica o e-mail, quando ci sono ricordi che non hai ancora guardato.

## Il futuro

**E se Immich cambia?**
I test del progetto girano contro un vero Immich, e il pulsante dentro Immich viene verificato contro le sue pagine sulla versione fissata, quindi un cambiamento emerge quando quella versione viene alzata. Se il pulsante non trova il suo posto, si nasconde da solo e nient’altro si rompe.

**E se Immich lo aggiunge da sé?**
Può aggiungerne una parte. Handoff usa solo l’API pubblica di Immich e non cambia nulla al suo interno; se Immich copre una parte di ciò per cui lo usi, puoi semplicemente smettere di usare quella parte e nient’altro ne risente. Ciò che resterebbe utile di per sé: scegliere se l’originale resta o viene spostato (e che nulla venga rimosso prima che esista una copia verificata), ospiti senza account che inviano tramite un codice QR con moderazione, un muro e una galleria, stanze cliente per i fotografi e la condivisione tra due server. Andarsene è facile: elimina la chiave in Immich, o premi **Esci**, e ogni foto resta dov’è.

## E poi

- Consegnare una prima foto: [Avvio rapido](../start/quickstart.md)
- Scegliere un obiettivo: [Uso](../using.md)
- Qualcosa non funziona: [Risoluzione dei problemi](../../reference/troubleshooting.md)
