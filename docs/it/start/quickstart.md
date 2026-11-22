---
title: "Avvio rapido: la tua prima consegna in dieci minuti"
tip: "Da zero alla prima consegna in dieci minuti."
source_rev: 77f7bc24
review: needed
---

> [!NOTE]
> Questa pagina è stata tradotta automaticamente e non è ancora stata rivista da una persona. Le immagini mostrano l’interfaccia in inglese. In caso di dubbio vale la versione inglese.

# Avvio rapido: la tua prima consegna in dieci minuti

Una foto che contrassegni con un tag nell’app Immich che già usi compare nella libreria di qualcun altro, con una copia verificata prima che il tuo originale si muova.

![Contrassegnare una foto, Immich lo segnala a immich-handoff, la foto arriva nella libreria del destinatario](../../images/flow.svg)

**Cosa ti serve:** Immich 3.2.0 o successivo, Docker sulla macchina che lo esegue e una decina di minuti. Se vuoi provarlo prima da solo, [avvia la demo](../../../demo/README.md): un comando, foto di esempio, nulla di tuo viene toccato.

## 1. Avviare l’app

Su Linux (su un Mac usa `sed -i ''`):

```
git clone --branch v1.3.1 https://github.com/vfuoglio/immich-handoff.git && cd immich-handoff
cp .env.example .env
sed -i -e "s/^WEBHOOK_SECRET=.*/WEBHOOK_SECRET=$(openssl rand -hex 24)/" -e "s/^HANDOFF_STORE_KEY=.*/HANDOFF_STORE_KEY=$(openssl rand -hex 24)/" .env
docker compose up -d
```

> [!TIP]
> Se la tua rete Immich non si chiama `immich_default`, imposta prima `IMMICH_NETWORK` nel file `.env`. **Tieni una copia di `HANDOFF_STORE_KEY`**: cifra le chiavi che l’app conserva per chi si unisce.

## 2. Accedere e offrire un servizio

Apri `http://<il-tuo-host>:8080/` con lo stesso nome host che usi per Immich e accedi con il tuo account Immich. Scegli **Offri un servizio tuo**, dagli un nome (per esempio «Vacanze 2026»), scegli il tag che useranno i mittenti (per esempio `share`) e chi può inviare. Nessuno crea a mano una chiave API: l’app ne crea una, limitata, in ogni account nel momento in cui qualcuno si unisce, e si può eliminare in Immich in qualsiasi momento.

![Il modulo per offrire un servizio](../../images/offer-form.png)

## 3. Lasciare che qualcuno si unisca

Condividi il link d’invito o il codice QR del servizio. L’altra persona lo apre, accede con il proprio account Immich e si unisce, scegliendo da sé cosa succede ai suoi originali: **tenerli** e inviare una copia, oppure **spostarli** nel cestino una volta che il tuo lato ha una copia verificata.

![Unirsi a un servizio e scegliere cosa succede ai tuoi originali](../../images/join-choice.png)

> [!WARNING]
> Scegli **tenere** finché fai prove. **Spostare** mette l’originale nel cestino del mittente dopo il controllo della copia.

## 4. Contrassegnare una foto

Nell’app Immich, aggiungi a una foto il tag `share`. Dopo pochi secondi è nella libreria del destinatario, con il tag `inbox`, e la pagina la mostra in viaggio. Se non arriva, esegui `docker compose exec immich-handoff /immich-handoff doctor`; indica cosa non va e come correggerlo.

![Le foto come arrivano nell’Immich del destinatario](../../images/immich-family.png)

> [!IMPORTANT]
> Tutte le persone coinvolte hanno bisogno dei **tag** attivati in Immich (Impostazioni dell’account, Funzioni, Tag) e devono ricaricare la pagina di Immich dopo. La pagina se ne accorge e offre un pulsante **Attiva i Tag**.

## E poi

- Smettere di contrassegnare a mano: [le regole](../../guides/automate/rules.md) e [il pulsante dentro Immich](../../guides/automate/immich-button.md)
- Raccogliere foto da ospiti a un evento: [le drop box](../../guides/collect/events.md)
- Inviare dal telefono: [la scheda Invia](../../guides/collect/phone.md)
- Orientarsi nella pagina: [la pagina](the-page.md)
- Gestirlo come si deve: [installazione](../../admin/install.md), poi [gestione](../../admin/operations.md)
