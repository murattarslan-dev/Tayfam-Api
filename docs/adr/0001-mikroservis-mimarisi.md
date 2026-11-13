# ADR-0001: Kubernetes üzerinde gRPC ile konuşan mikroservisler

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Arslan

## Bağlam

Tayfam; kulüp ve arkadaş gruplarının (tayfaların) etkinlik, ilan, soru-cevap ve
sohbet ihtiyaçlarını karşılayan bir uygulama. Beklenen kullanım birkaç kulüp,
en fazla birkaç bin kullanıcı. Tek kişilik bir projedir ve kendi yönettiğimiz
tek bir VPS üzerinde çalışacaktır.

Bu ölçekte işlevsel olarak en uygun seçenek modüler bir monolittir. Ancak proje
bir hobi projesidir ve **birincil amaçlardan biri dağıtık sistem tecrübesi
kazanmaktır**: mikroservis sınırları, servisler arası gRPC, API gateway ve
Kubernetes işletimi.

## Karar

API katmanı, Kubernetes üzerinde container olarak çalışan mikroservislerden
oluşacak:

- Servisler kendi aralarında **gRPC** ile konuşur.
- Dış dünyaya yalnızca **API gateway** açılır; iç servisler cluster dışına
  erişilebilir olmaz.
- Tüm servisler bu repoda (monorepo) yaşar.

Servislerin sayısı ve sınırları, veri sahipliği, gateway'in dış sözleşmesi ve
Kubernetes dağıtımı ayrı ADR'lerde karara bağlanacak.

## Gerekçe

- Bu bilinçli bir öğrenme tercihidir; ihtiyaçtan doğmamıştır. Mevcut ölçek için
  aşırı mühendislik olduğu baştan kabul edilmiştir.
- Gerçek bir ürün üzerinde öğrenmek; servis sınırlarını, dağıtık veri
  tutarlılığını ve işletme yükünü gerçek ihtiyaçlarla sınar.

## Sonuçlar

**Kazanımlar**

- gRPC, protobuf sözleşmeleri, gateway deseni, Kubernetes ve dağıtık izleme
  (tracing) üzerine uygulamalı tecrübe.
- Servisler bağımsız deploy edilebilir ve ölçeklenebilir.

**Bedeller (bilinçli kabul edilenler)**

- Tek node'da Kubernetes yüksek erişilebilirlik sağlamaz; control plane ek
  RAM ve bakım yükü getirir.
- Servisler arası işlemler tek transaction olmaktan çıkar. Tutarlılık için
  event veya saga desenleri gerekir.
- Yerel geliştirme, test ve gözlemlenebilirlik daha karmaşık hale gelir.
- Bir özellik birden fazla servise ve proto değişikliğine dokunabilir. Bu
  durum PR'ları büyütür.

**Bedelleri azaltma ilkeleri**

- Servis sayısı az tutulur; yeni servis ancak net bir sınır varsa açılır.
- Dağıtık veri problemleri bilerek minimumda tutulur.
- İleride gerekirse bu karar, servisleri tek binary'de birleştirerek geri
  alınabilecek şekilde uygulanır: iş mantığı taşıma katmanından (gRPC/HTTP)
  bağımsız yazılır.

## Değerlendirilen alternatifler

- **Modüler monolit + Docker Compose:** Bu ölçek için en pratik çözüm. Öğrenme
  hedefini karşılamadığı için seçilmedi.
