# Documentation images

The pictures in `docs/images/` are made from a real, running app by this small tool, so they can be
remade whenever the page changes. Nothing here is needed to run immich-handoff.

Needs Docker, Google Chrome (or Chromium) and Go. It uses its own throwaway Immich on port 2294 and the app on 8090, so a demo on the usual ports is left alone.

```
cd tools/screenshots && export IMMICH_URL=http://localhost:2294 APP_URL=http://localhost:8090/
./seed.sh reset                  # fresh Immich with users alice, bob and family (password Passw0rd!x)
DRY=1 ./seed.sh setup            # run the app, create people and services, make sample photos
./seed.sh waiting                # upload photos and tag some; the dry-run app leaves them waiting
go run . shots sender ../../docs/images && go run . hero ../../docs/images
./seed.sh handover               # restart the app for real: the photos are handed over
go run . shots owner ../../docs/images && go run . shots immich ../../docs/images
./seed.sh lost                   # Carol deletes her key
go run . shots lost ../../docs/images
```

The sample photos are generated landscapes (`go run . gen <dir>`), so no real pictures or people appear.

Every picture is taken in English: the tool sends `Accept-Language: en` and sets the page's language cookie to English, whatever language the machine uses. The pictures belong to the English documentation.
