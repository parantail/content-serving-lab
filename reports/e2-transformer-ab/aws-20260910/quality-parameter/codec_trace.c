#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdio.h>
#include <libheif/heif.h>

struct heif_error heif_context_encode_image(struct heif_context *ctx,
    const struct heif_image *image, struct heif_encoder *encoder,
    const struct heif_encoding_options *options, struct heif_image_handle **handle) {
  typedef struct heif_error (*encode_fn)(struct heif_context *, const struct heif_image *,
      struct heif_encoder *, const struct heif_encoding_options *, struct heif_image_handle **);
  encode_fn original = (encode_fn)dlsym(RTLD_NEXT, "heif_context_encode_image");
  int threads=-1, speed=-1, quality=-1;
  struct heif_error t=heif_encoder_get_parameter_integer(encoder,"threads",&threads);
  struct heif_error s=heif_encoder_get_parameter_integer(encoder,"speed",&speed);
  struct heif_error q=heif_encoder_get_parameter_integer(encoder,"quality",&quality);
  fprintf(stderr,"E2_QUALITY actual_quality=%d query_error=%d\n",quality,q.code);
  fprintf(stderr,"E2_CODEC encoder=%s threads=%d speed=%d query_errors=%d,%d\n",
      heif_encoder_get_name(encoder), threads, speed, t.code, s.code);
  return original(ctx,image,encoder,options,handle);
}
